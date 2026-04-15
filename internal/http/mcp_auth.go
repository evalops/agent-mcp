package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
)

const mcpRequestInspectLimit int64 = 1 << 20

type mcpAuthContextKey struct{}

type mcpRequestInfo struct {
	Method    string
	ToolName  string
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

func newMCPAuthMiddleware(cfg config.Config, identityClient *clients.IdentityClient, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}

			info, err := inspectMCPRequest(r)
			if err != nil {
				logger.Warn("inspect mcp request", "error", err)
			}
			if info.UserToken != "" {
				next.ServeHTTP(w, r)
				return
			}

			token := bearerTokenFromHeader(r.Header.Get("Authorization"))
			if token == "" {
				writeMCPUnauthorized(w, r, cfg)
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
			next.ServeHTTP(w, r.WithContext(ctx))
		})
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
		info.UserToken = strings.TrimSpace(args.UserToken)
	}
	return info, nil
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

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
