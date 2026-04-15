package clients

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegisterAgent(t *testing.T) {
	expected := AgentSession{
		AgentID:       "agent_test123",
		AgentToken:    "tok_abc",
		ExpiresAt:     time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		RunID:         "run_xyz",
		ScopesGranted: []string{"governance:read"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/agents/register" {
			t.Fatalf("expected /v1/agents/register, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer user_tok" {
			t.Fatalf("expected bearer user_tok, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("expected application/json, got %s", r.Header.Get("Content-Type"))
		}

		var req RegisterAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.AgentType != "claude-code" {
			t.Fatalf("expected claude-code, got %s", req.AgentType)
		}
		if req.Surface != "cli" {
			t.Fatalf("expected cli, got %s", req.Surface)
		}

		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(expected); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	client := NewIdentityClient(srv.URL, srv.Client(), 5*time.Second)
	session, err := client.RegisterAgent(context.Background(), "user_tok", RegisterAgentRequest{
		AgentType: "claude-code",
		Surface:   "cli",
		Scopes:    []string{"governance:read"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.AgentID != expected.AgentID {
		t.Fatalf("expected %s, got %s", expected.AgentID, session.AgentID)
	}
	if session.AgentToken != expected.AgentToken {
		t.Fatalf("expected %s, got %s", expected.AgentToken, session.AgentToken)
	}
	if session.RunID != expected.RunID {
		t.Fatalf("expected %s, got %s", expected.RunID, session.RunID)
	}
}

func TestRegisterAgentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		if _, err := w.Write([]byte(`{"error":"invalid_token"}`)); err != nil {
			t.Fatalf("write error response: %v", err)
		}
	}))
	defer srv.Close()

	client := NewIdentityClient(srv.URL, srv.Client(), 5*time.Second)
	_, err := client.RegisterAgent(context.Background(), "bad_tok", RegisterAgentRequest{
		AgentType: "claude-code",
		Surface:   "cli",
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", httpErr.StatusCode)
	}
}

func TestFederateAgent(t *testing.T) {
	expected := AgentSession{
		AgentID:       "agent_federated123",
		AgentToken:    "tok_federated",
		ExpiresAt:     time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		RunID:         "run_federated",
		ScopesGranted: []string{"governance:read"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/agents/federate" {
			t.Fatalf("expected /v1/agents/federate, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("expected application/json, got %s", r.Header.Get("Content-Type"))
		}

		var req FederateAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Provider != "openai" {
			t.Fatalf("expected provider openai, got %s", req.Provider)
		}
		if req.ExternalToken != "provider_tok" {
			t.Fatalf("expected provider token, got %s", req.ExternalToken)
		}
		if req.OrganizationID != "ws_123" {
			t.Fatalf("expected ws_123, got %s", req.OrganizationID)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(expected)
	}))
	defer srv.Close()

	client := NewIdentityClient(srv.URL, srv.Client(), 5*time.Second)
	session, err := client.FederateAgent(context.Background(), FederateAgentRequest{
		AgentType:      "codex",
		ExternalToken:  "provider_tok",
		OrganizationID: "ws_123",
		Provider:       "openai",
		Surface:        "cli",
		Scopes:         []string{"governance:read"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.AgentID != expected.AgentID {
		t.Fatalf("expected %s, got %s", expected.AgentID, session.AgentID)
	}
	if session.AgentToken != expected.AgentToken {
		t.Fatalf("expected %s, got %s", expected.AgentToken, session.AgentToken)
	}
}

func TestHeartbeatAgent(t *testing.T) {
	expected := AgentSession{
		AgentID:    "agent_test123",
		AgentToken: "tok_rotated",
		ExpiresAt:  time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC),
		RunID:      "run_xyz",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/heartbeat" {
			t.Fatalf("expected /v1/agents/heartbeat, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok_abc" {
			t.Fatalf("expected bearer tok_abc, got %s", r.Header.Get("Authorization"))
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode heartbeat payload: %v", err)
		}
		if int(payload["ttl_seconds"].(float64)) != 1800 {
			t.Fatalf("expected ttl_seconds 1800, got %v", payload["ttl_seconds"])
		}

		if err := json.NewEncoder(w).Encode(expected); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	client := NewIdentityClient(srv.URL, srv.Client(), 5*time.Second)
	session, err := client.HeartbeatAgent(context.Background(), "tok_abc", 1800)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.AgentToken != "tok_rotated" {
		t.Fatalf("expected tok_rotated, got %s", session.AgentToken)
	}
}

func TestDeregisterAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/deregister" {
			t.Fatalf("expected /v1/agents/deregister, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok_abc" {
			t.Fatalf("expected bearer tok_abc")
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"revoked":true}`)); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer srv.Close()

	client := NewIdentityClient(srv.URL, srv.Client(), 5*time.Second)
	err := client.DeregisterAgent(context.Background(), "tok_abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeregisterAgentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		if _, err := w.Write([]byte(`{"error":"session_not_found"}`)); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer srv.Close()

	client := NewIdentityClient(srv.URL, srv.Client(), 5*time.Second)
	err := client.DeregisterAgent(context.Background(), "tok_gone")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestNewIdentityClientDefaultHTTP(t *testing.T) {
	client := NewIdentityClient("http://example.com", nil, 5*time.Second)
	if client.httpClient == nil {
		t.Fatal("expected default http client")
	}
}

func TestIntrospectToken(t *testing.T) {
	expectedAudience := []string{"https://mcp.evalops.dev"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tokens/introspect" {
			t.Fatalf("expected /v1/tokens/introspect, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer header.token.sig" {
			t.Fatalf("expected Authorization bearer header.token.sig, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("expected application/json, got %s", r.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if strings.TrimSpace(string(body)) != "{}" {
			t.Fatalf("expected {} body, got %q", string(body))
		}
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(TokenIntrospection{
			Active:         true,
			Audience:       expectedAudience,
			OrganizationID: "org_123",
			Scopes:         []string{"agent:register"},
			Subject:        "user_123",
			TokenType:      "access",
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	client := NewIdentityClient(srv.URL, srv.Client(), 5*time.Second)
	introspection, err := client.IntrospectToken(context.Background(), "header.token.sig")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !introspection.Active {
		t.Fatal("expected active token")
	}
	if introspection.OrganizationID != "org_123" {
		t.Fatalf("expected org_123, got %s", introspection.OrganizationID)
	}
	if len(introspection.Audience) != 1 || introspection.Audience[0] != expectedAudience[0] {
		t.Fatalf("expected audience %v, got %v", expectedAudience, introspection.Audience)
	}
}

func TestIntrospectTokenFallsBackToJWTAudience(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(TokenIntrospection{
			Active: true,
			Scopes: []string{"agent:register"},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	client := NewIdentityClient(srv.URL, srv.Client(), 5*time.Second)
	token := strings.Join([]string{
		"header",
		"eyJhdWQiOlsiaHR0cHM6Ly9tY3AuZXZhbG9wcy5kZXYiXX0",
		"sig",
	}, ".")
	introspection, err := client.IntrospectToken(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(introspection.Audience) != 1 || introspection.Audience[0] != "https://mcp.evalops.dev" {
		t.Fatalf("expected JWT audience fallback, got %v", introspection.Audience)
	}
}
