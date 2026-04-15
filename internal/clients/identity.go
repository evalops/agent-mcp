package clients

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// IdentityClient calls the Identity service REST endpoints for agent lifecycle.
type IdentityClient struct {
	baseURL       string
	introspectURL string
	httpClient    *http.Client
	timeout       time.Duration
}

type RegisterAgentRequest struct {
	AgentType    string         `json:"agent_type"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Scopes       []string       `json:"scopes,omitempty"`
	Surface      string         `json:"surface"`
	TTLSeconds   int            `json:"ttl_seconds,omitempty"`
}

type FederateAgentRequest struct {
	AgentType      string         `json:"agent_type"`
	Capabilities   []string       `json:"capabilities,omitempty"`
	ExternalToken  string         `json:"external_token"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	OrganizationID string         `json:"organization_id"`
	Provider       string         `json:"provider"`
	Scopes         []string       `json:"scopes,omitempty"`
	Surface        string         `json:"surface"`
	TTLSeconds     int            `json:"ttl_seconds,omitempty"`
}

type AgentSession struct {
	AgentID         string    `json:"agent_id"`
	AgentToken      string    `json:"agent_token"`
	ExpiresAt       time.Time `json:"expires_at"`
	RunID           string    `json:"run_id"`
	ScopesDenied    []string  `json:"scopes_denied,omitempty"`
	ScopesGranted   []string  `json:"scopes_granted,omitempty"`
	ScopesRequested []string  `json:"scopes_requested,omitempty"`
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("identity returned %d: %s", e.StatusCode, e.Body)
}

type TokenIntrospection struct {
	Active         bool      `json:"active"`
	Audience       []string  `json:"audience,omitempty"`
	Email          string    `json:"email,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	IssuedAt       time.Time `json:"issued_at,omitempty"`
	Name           string    `json:"name,omitempty"`
	OrganizationID string    `json:"organization_id,omitempty"`
	Picture        string    `json:"picture,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Scopes         []string  `json:"scopes,omitempty"`
	Service        string    `json:"service,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	TenantID       string    `json:"tenant_id,omitempty"`
	TokenType      string    `json:"token_type,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type CreateAPIKeyRequest struct {
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes,omitempty"`
}

type APIKey struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	Name           string     `json:"name"`
	Prefix         string     `json:"prefix"`
	Role           string     `json:"role"`
	Provider       string     `json:"provider,omitempty"`
	Label          string     `json:"label,omitempty"`
	Scopes         []string   `json:"scopes"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

type CreateAPIKeyResponse struct {
	APIKey          string   `json:"api_key"`
	Key             APIKey   `json:"key"`
	ScopesDenied    []string `json:"scopes_denied,omitempty"`
	ScopesGranted   []string `json:"scopes_granted,omitempty"`
	ScopesRequested []string `json:"scopes_requested,omitempty"`
}

func NewIdentityClient(baseURL string, httpClient *http.Client, timeout time.Duration) *IdentityClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &IdentityClient{baseURL: baseURL, httpClient: httpClient, timeout: timeout}
}

func (c *IdentityClient) WithIntrospectURL(introspectURL string) *IdentityClient {
	if c == nil {
		return nil
	}
	c.introspectURL = strings.TrimSpace(introspectURL)
	return c
}

func (c *IdentityClient) RegisterAgent(ctx context.Context, userToken string, req RegisterAgentRequest) (AgentSession, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return AgentSession{}, fmt.Errorf("marshal register request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/agents/register", bytes.NewReader(body))
	if err != nil {
		return AgentSession{}, fmt.Errorf("build register request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	return c.doAgentSessionRequest(httpReq)
}

func (c *IdentityClient) FederateAgent(ctx context.Context, req FederateAgentRequest) (AgentSession, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return AgentSession{}, fmt.Errorf("marshal federate request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/agents/federate", bytes.NewReader(body))
	if err != nil {
		return AgentSession{}, fmt.Errorf("build federate request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	return c.doAgentSessionRequest(httpReq)
}

func (c *IdentityClient) HeartbeatAgent(ctx context.Context, agentToken string, ttlSeconds int) (AgentSession, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	payload := map[string]any{}
	if ttlSeconds > 0 {
		payload["ttl_seconds"] = ttlSeconds
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/agents/heartbeat", bytes.NewReader(body))
	if err != nil {
		return AgentSession{}, fmt.Errorf("build heartbeat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+agentToken)

	return c.doAgentSessionRequest(httpReq)
}

func (c *IdentityClient) DeregisterAgent(ctx context.Context, agentToken string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/agents/deregister", http.NoBody)
	if err != nil {
		return fmt.Errorf("build deregister request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+agentToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("deregister agent: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		return readErrorResponse(resp)
	}
	return nil
}

func (c *IdentityClient) IntrospectToken(ctx context.Context, token string) (TokenIntrospection, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	introspectURL := c.introspectURL
	if introspectURL == "" {
		introspectURL = strings.TrimRight(c.baseURL, "/") + "/v1/tokens/introspect"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, introspectURL, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return TokenIntrospection{}, fmt.Errorf("build introspect request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return TokenIntrospection{}, fmt.Errorf("introspect token: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		return TokenIntrospection{}, readErrorResponse(resp)
	}
	var result TokenIntrospection
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return TokenIntrospection{}, fmt.Errorf("decode introspection response: %w", err)
	}
	if len(result.Audience) == 0 {
		result.Audience = decodeJWTAudience(token)
	}
	return result, nil
}

func (c *IdentityClient) CreateAPIKey(ctx context.Context, userToken string, req CreateAPIKeyRequest) (CreateAPIKeyResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return CreateAPIKeyResponse{}, fmt.Errorf("marshal api key request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/api-keys", bytes.NewReader(body))
	if err != nil {
		return CreateAPIKeyResponse{}, fmt.Errorf("build api key request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return CreateAPIKeyResponse{}, fmt.Errorf("create api key: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		return CreateAPIKeyResponse{}, readErrorResponse(resp)
	}
	var created CreateAPIKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return CreateAPIKeyResponse{}, fmt.Errorf("decode create api key response: %w", err)
	}
	return created, nil
}

func (c *IdentityClient) ListAPIKeys(ctx context.Context, userToken string) ([]APIKey, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/api-keys", nil)
	if err != nil {
		return nil, fmt.Errorf("build list api keys request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		return nil, readErrorResponse(resp)
	}
	var payload struct {
		APIKeys []APIKey `json:"api_keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode list api keys response: %w", err)
	}
	return payload.APIKeys, nil
}

func (c *IdentityClient) doAgentSessionRequest(req *http.Request) (AgentSession, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return AgentSession{}, fmt.Errorf("identity request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 400 {
		return AgentSession{}, readErrorResponse(resp)
	}
	var session AgentSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return AgentSession{}, fmt.Errorf("decode identity response: %w", err)
	}
	return session, nil
}

func readErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
}

func decodeJWTAudience(token string) []string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims struct {
		Audience any `json:"aud"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	switch audience := claims.Audience.(type) {
	case string:
		if audience == "" {
			return nil
		}
		return []string{audience}
	case []any:
		values := make([]string, 0, len(audience))
		for _, raw := range audience {
			value, ok := raw.(string)
			if ok && value != "" {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			return nil
		}
		return values
	default:
		return nil
	}
}
