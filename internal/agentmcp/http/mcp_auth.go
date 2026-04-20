package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/evalops/agent-mcp/internal/agentmcp/agentmcp"
	"github.com/evalops/agent-mcp/internal/agentmcp/clients"
	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	"github.com/evalops/service-runtime/authmw"
)

const mcpRequestInspectLimit int64 = 1 << 20

type mcpAuthContextKey struct{}

type mcpRequestInfo struct {
	Method    string
	ToolName  string
	AgentType string
	UserToken string
}

type mcpToolCallEnvelope struct {
	Method string `json:"method"`
	Params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"params"`
}

type mcpRegisterArguments struct {
	AgentType string `json:"agent_type"`
	UserToken string `json:"user_token"`
}

var mcpToolScopes = map[string]string{
	"evalops_check_action":   "governance:evaluate",
	"evalops_check_approval": "governance:evaluate",
	"evalops_deregister":     "agent:register",
	"evalops_heartbeat":      "agent:heartbeat",
	"evalops_register":       "agent:register",
	"evalops_report_usage":   "meter:record",
}

const (
	anonymousSessionTTL     = time.Hour
	anonymousPerMinuteLimit = 10
	anonymousPerHourLimit   = 100
)

type anonymousAccessLimiter struct {
	mu      sync.Mutex
	entries map[string]*anonymousAccessWindow
}

type anonymousAccessWindow struct {
	hourCount         int
	hourWindowStart   time.Time
	minuteCount       int
	minuteWindowStart time.Time
}

func newProtectedResourceMetadataHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		issuer := strings.TrimRight(strings.TrimSpace(cfg.Identity.IssuerURL), "/")
		if issuer == "" {
			issuer = strings.TrimRight(strings.TrimSpace(cfg.Identity.BaseURL), "/")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"authorization_servers": []string{issuer},
			"bearer_methods_supported": []string{
				"header",
			},
			"resource":         protectedResourceURL(r, cfg),
			"scopes_supported": supportedScopes(),
		})
	}
}

func newMCPAuthMiddleware(cfg config.Config, identityClient *clients.IdentityClient, sessions agentmcp.SessionBackend, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	limiter := newAnonymousAccessLimiter()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}

			info, err := inspectMCPRequest(r)
			if err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
				logger.Warn("inspect mcp request", "error", err)
			}
			if info.UserToken != "" {
				next.ServeHTTP(w, r)
				return
			}

			now := time.Now().UTC()
			if state, ok := activeSessionState(sessions, strings.TrimSpace(r.Header.Get("Mcp-Session-Id")), now); ok {
				if state.IsAnonymous() {
					if !limiter.Allow(clientAddress(r), now) {
						writeMCPRateLimited(w)
						return
					}
					r.Header.Set(agentmcp.AnonymousSandboxHeader, "true")
				}
				next.ServeHTTP(w, r)
				return
			}

			token := bearerTokenFromHeader(r.Header.Get("Authorization"))
			if token == "" {
				if info.Method == "tools/call" && info.ToolName == "evalops_register" && hasConfiguredFederationCredential(cfg.Federation, info.AgentType) {
					next.ServeHTTP(w, r)
					return
				}
				if !limiter.Allow(clientAddress(r), now) {
					writeMCPRateLimited(w)
					return
				}
				if !ensureAnonymousSession(sessions, strings.TrimSpace(r.Header.Get("Mcp-Session-Id")), now, cfg.Session.MaxActive) {
					writeMCPSessionLimitExceeded(w)
					return
				}
				r.Header.Set(agentmcp.AnonymousSandboxHeader, "true")
				next.ServeHTTP(w, r)
				return
			}

			introspection, err := identityClient.IntrospectToken(r.Context(), token)
			if err != nil {
				logger.Warn("token introspection failed", "error", err)
				writeMCPUnauthorized(w, r, cfg)
				return
			}
			if !introspection.Active {
				writeMCPUnauthorized(w, r, cfg)
				return
			}
			resourceURL := configuredProtectedResourceURL(cfg)
			if resourceURL == "" {
				logger.Warn("protected resource URL is not configured")
				writeMCPUnauthorized(w, r, cfg)
				return
			}
			if !audienceMatchesResource(introspection.Audience, resourceURL) {
				logger.Warn("token audience does not match protected resource", "audience", introspection.Audience)
				writeMCPUnauthorized(w, r, cfg)
				return
			}

			requiredScope := requiredScopeForRequest(info)
			if requiredScope != "" && !hasScope(introspection.Scopes, requiredScope) {
				writeMCPInsufficientScope(w, requiredScope)
				return
			}

			ctx := context.WithValue(r.Context(), mcpAuthContextKey{}, introspection)
			ctx = authmw.ContextWithPrincipal(ctx, principalFromIntrospection(introspection))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func principalFromIntrospection(introspection clients.TokenIntrospection) authmw.Principal {
	organizationID := strings.TrimSpace(introspection.OrganizationID)
	if organizationID == "" {
		organizationID = strings.TrimSpace(introspection.TenantID)
	}
	tokenType := strings.TrimSpace(introspection.TokenType)
	service := strings.TrimSpace(introspection.Service)
	if tokenType == "" {
		if service != "" {
			tokenType = "service"
		} else {
			tokenType = "bearer"
		}
	}
	subject := strings.TrimSpace(introspection.Subject)
	if subject == "" && service != "" {
		subject = service
	}
	return authmw.Principal{
		OrganizationID: organizationID,
		WorkspaceID:    organizationID,
		Subject:        subject,
		Service:        service,
		TokenType:      tokenType,
		Scopes:         append([]string(nil), introspection.Scopes...),
		IsHuman:        strings.EqualFold(tokenType, "human") || strings.EqualFold(tokenType, "user"),
	}
}

func inspectMCPRequest(r *http.Request) (mcpRequestInfo, error) {
	if r.Body == nil {
		return mcpRequestInfo{}, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpRequestInspectLimit))
	if err != nil {
		return mcpRequestInfo{}, fmt.Errorf("read request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		return mcpRequestInfo{}, nil
	}

	var envelope mcpToolCallEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return mcpRequestInfo{}, fmt.Errorf("decode mcp envelope: %w", err)
	}
	info := mcpRequestInfo{
		Method:   strings.TrimSpace(envelope.Method),
		ToolName: strings.TrimSpace(envelope.Params.Name),
	}
	if info.Method == "tools/call" && info.ToolName == "evalops_register" && len(envelope.Params.Arguments) > 0 {
		var args mcpRegisterArguments
		if err := json.Unmarshal(envelope.Params.Arguments, &args); err != nil {
			return info, fmt.Errorf("decode evalops_register arguments: %w", err)
		}
		info.AgentType = strings.TrimSpace(args.AgentType)
		info.UserToken = strings.TrimSpace(args.UserToken)
	}
	return info, nil
}

func newAnonymousAccessLimiter() *anonymousAccessLimiter {
	return &anonymousAccessLimiter{entries: make(map[string]*anonymousAccessWindow)}
}

func (l *anonymousAccessLimiter) Allow(key string, now time.Time) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "unknown"
	}
	minuteStart := now.UTC().Truncate(time.Minute)
	hourStart := now.UTC().Truncate(time.Hour)

	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[key]
	if !ok {
		entry = &anonymousAccessWindow{}
		l.entries[key] = entry
	}
	if entry.minuteWindowStart != minuteStart {
		entry.minuteWindowStart = minuteStart
		entry.minuteCount = 0
	}
	if entry.hourWindowStart != hourStart {
		entry.hourWindowStart = hourStart
		entry.hourCount = 0
	}
	if entry.minuteCount >= anonymousPerMinuteLimit || entry.hourCount >= anonymousPerHourLimit {
		return false
	}
	entry.minuteCount++
	entry.hourCount++
	return true
}

func activeSessionState(sessions agentmcp.SessionBackend, sessionID string, now time.Time) (*agentmcp.SessionState, bool) {
	if sessions == nil || strings.TrimSpace(sessionID) == "" {
		return nil, false
	}
	state, ok := sessions.Get(sessionID)
	if !ok || state == nil {
		return nil, false
	}
	if !state.ExpiresAt.IsZero() && state.ExpiresAt.Before(now) {
		sessions.Delete(sessionID)
		return nil, false
	}
	return state, true
}

func ensureAnonymousSession(sessions agentmcp.SessionBackend, sessionID string, now time.Time, maxActive int) bool {
	if sessions == nil || strings.TrimSpace(sessionID) == "" {
		return true
	}
	if state, ok := activeSessionState(sessions, sessionID, now); ok && !state.IsAnonymous() {
		return true
	} else if ok && state.IsAnonymous() {
		return true
	}
	return sessions.SetIfUnderLimit(sessionID, &agentmcp.SessionState{
		ExpiresAt:   now.Add(anonymousSessionTTL),
		SessionType: agentmcp.SessionTypeAnonymous,
		Surface:     "mcp",
	}, maxActive)
}

func hasConfiguredFederationCredential(federation config.FederationConfig, agentType string) bool {
	providers := configuredFederationProviders(federation)
	if len(providers) == 0 {
		return false
	}
	if provider := inferFederationProvider(agentType); provider != "" {
		_, ok := providers[provider]
		return ok
	}
	return len(providers) == 1
}

func configuredFederationProviders(federation config.FederationConfig) map[string]string {
	configured := make(map[string]string, 4)
	if strings.TrimSpace(federation.AnthropicAPIKey) != "" {
		configured["anthropic"] = federation.AnthropicAPIKey
	}
	if strings.TrimSpace(federation.OpenAIAPIKey) != "" {
		configured["openai"] = federation.OpenAIAPIKey
	}
	if strings.TrimSpace(federation.GitHubToken) != "" {
		configured["github"] = federation.GitHubToken
	}
	if strings.TrimSpace(federation.GoogleAccessToken) != "" {
		configured["google"] = federation.GoogleAccessToken
	}
	return configured
}

func inferFederationProvider(agentType string) string {
	normalized := strings.ToLower(strings.TrimSpace(agentType))
	switch {
	case strings.Contains(normalized, "claude"), strings.Contains(normalized, "anthropic"):
		return "anthropic"
	case strings.Contains(normalized, "codex"), strings.Contains(normalized, "openai"):
		return "openai"
	case strings.Contains(normalized, "copilot"), strings.Contains(normalized, "github"):
		return "github"
	case strings.Contains(normalized, "gemini"), strings.Contains(normalized, "google"):
		return "google"
	default:
		return ""
	}
}

func clientAddress(r *http.Request) string {
	if r == nil {
		return ""
	}
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwarded != "" {
		if parts := strings.Split(forwarded, ","); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func requiredScopeForRequest(info mcpRequestInfo) string {
	if info.Method != "tools/call" {
		return ""
	}
	return mcpToolScopes[info.ToolName]
}

func supportedScopes() []string {
	return []string{
		"agent:heartbeat",
		"agent:register",
		"governance:evaluate",
		"meter:record",
	}
}

func bearerTokenFromHeader(authorization string) string {
	authorization = strings.TrimSpace(authorization)
	if !strings.HasPrefix(authorization, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
}

func protectedResourceURL(r *http.Request, cfg config.Config) string {
	if configured := configuredProtectedResourceURL(cfg); configured != "" {
		return configured
	}
	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	return strings.TrimRight(scheme+"://"+host, "/")
}

func configuredProtectedResourceURL(cfg config.Config) string {
	return strings.TrimRight(strings.TrimSpace(cfg.ResourceURL), "/")
}

func audienceMatchesResource(audience []string, resource string) bool {
	resource = strings.TrimRight(strings.TrimSpace(resource), "/")
	if resource == "" {
		return true
	}
	if len(audience) == 0 {
		return false
	}
	for _, candidate := range audience {
		if strings.TrimRight(strings.TrimSpace(candidate), "/") == resource {
			return true
		}
	}
	return false
}

func hasScope(scopes []string, required string) bool {
	required = strings.TrimSpace(required)
	if required == "" {
		return true
	}
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == required {
			return true
		}
	}
	return false
}

func writeMCPUnauthorized(w http.ResponseWriter, r *http.Request, cfg config.Config) {
	resourceMetadata := protectedResourceURL(r, cfg) + "/.well-known/oauth-protected-resource"
	w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer resource_metadata=%q", resourceMetadata))
	writeJSON(w, http.StatusUnauthorized, map[string]string{
		"error":   "unauthorized",
		"message": "Authentication required. MCP clients will handle this automatically.",
	})
}

func writeMCPInsufficientScope(w http.ResponseWriter, scope string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer error=%q scope=%q", "insufficient_scope", scope))
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":   "insufficient_scope",
		"message": "Additional authorization required.",
		"scope":   scope,
	})
}

func writeMCPRateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeJSON(w, http.StatusTooManyRequests, map[string]string{
		"error":   "rate_limited",
		"message": "Anonymous sandbox rate limit exceeded. Retry in about a minute or authenticate for a full session.",
	})
}

func writeMCPSessionLimitExceeded(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeJSON(w, http.StatusTooManyRequests, map[string]string{
		"error":   "session_limit_exceeded",
		"message": "Active MCP session limit exceeded. Retry after existing sessions expire or deregister.",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
