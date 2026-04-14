package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// IdentityClient calls the Identity service REST endpoints for agent lifecycle.
type IdentityClient struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
}

type RegisterAgentRequest struct {
	AgentType    string            `json:"agent_type"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	Scopes       []string          `json:"scopes,omitempty"`
	Surface      string            `json:"surface"`
	TTLSeconds   int               `json:"ttl_seconds,omitempty"`
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

func NewIdentityClient(baseURL string, httpClient *http.Client, timeout time.Duration) *IdentityClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &IdentityClient{baseURL: baseURL, httpClient: httpClient, timeout: timeout}
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
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readErrorResponse(resp)
	}
	return nil
}

func (c *IdentityClient) doAgentSessionRequest(req *http.Request) (AgentSession, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return AgentSession{}, fmt.Errorf("identity request failed: %w", err)
	}
	defer resp.Body.Close()
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
	return fmt.Errorf("identity returned %d: %s", resp.StatusCode, string(body))
}
