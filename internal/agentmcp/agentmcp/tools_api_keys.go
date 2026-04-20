package agentmcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/evalops/agent-mcp/internal/agentmcp/clients"
	"github.com/evalops/service-runtime/downstream"
	"github.com/evalops/service-runtime/resilience"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const apiKeyWarning = "This is the only time the full key will be shown. Store it securely."

type createAPIKeyInput struct {
	ExpiresInDays int      `json:"expires_in_days,omitempty" jsonschema:"Optional expiry in days. Omit for no expiry"`
	Name          string   `json:"name" jsonschema:"required,Human-readable API key name such as github-actions-prod"`
	Scopes        []string `json:"scopes,omitempty" jsonschema:"Optional scope restrictions. Defaults to the creating user's scopes"`
}

type createAPIKeyOutput struct {
	APIKey    string   `json:"api_key"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	KeyID     string   `json:"key_id"`
	Name      string   `json:"name"`
	Prefix    string   `json:"prefix"`
	Scopes    []string `json:"scopes"`
	Warning   string   `json:"warning"`
}

type listAPIKeysOutput struct {
	Keys []apiKeyListItem `json:"keys"`
}

type apiKeyListItem struct {
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	KeyID      string   `json:"key_id"`
	LastUsedAt string   `json:"last_used_at,omitempty"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
}

func (rc *requestContext) toolCreateAPIKey(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input createAPIKeyInput,
) (*mcpsdk.CallToolResult, createAPIKeyOutput, error) {
	if rc.isAnonymousRequest() {
		return rc.authenticationRequiredResult("create an API key"), createAPIKeyOutput{}, nil
	}
	bearerToken := rc.bearerToken()
	if bearerToken == "" {
		return nil, createAPIKeyOutput{}, fmt.Errorf("missing user bearer token")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, createAPIKeyOutput{}, fmt.Errorf("name is required")
	}
	if input.ExpiresInDays < 0 {
		return nil, createAPIKeyOutput{}, fmt.Errorf("expires_in_days must be zero or greater")
	}

	var expiresAt *time.Time
	if input.ExpiresInDays > 0 {
		value := time.Now().UTC().AddDate(0, 0, input.ExpiresInDays)
		expiresAt = &value
	}

	created, err := downstream.CallOp(ctx, rc.deps.downstreamClients().Identity, "create_api_key", func(ctx context.Context) (clients.CreateAPIKeyResponse, error) {
		return rc.deps.Identity.CreateAPIKey(ctx, bearerToken, clients.CreateAPIKeyRequest{
			ExpiresAt: expiresAt,
			Name:      name,
			Scopes:    input.Scopes,
		})
	})
	if err != nil {
		if errors.Is(err, resilience.ErrCircuitOpen) {
			return nil, createAPIKeyOutput{}, fmt.Errorf("identity service unreachable (circuit breaker open)")
		}
		return nil, createAPIKeyOutput{}, fmt.Errorf("create api key failed: %w", err)
	}

	grantedScopes := created.ScopesGranted
	if len(grantedScopes) == 0 {
		grantedScopes = created.Key.Scopes
	}

	return nil, createAPIKeyOutput{
		APIKey:    created.APIKey,
		ExpiresAt: formatOptionalTime(created.Key.ExpiresAt),
		KeyID:     created.Key.ID,
		Name:      created.Key.Name,
		Prefix:    created.Key.Prefix,
		Scopes:    append([]string(nil), grantedScopes...),
		Warning:   apiKeyWarning,
	}, nil
}

func (rc *requestContext) toolListAPIKeys(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	_ struct{},
) (*mcpsdk.CallToolResult, listAPIKeysOutput, error) {
	if rc.isAnonymousRequest() {
		return rc.authenticationRequiredResult("list API keys"), listAPIKeysOutput{}, nil
	}
	bearerToken := rc.bearerToken()
	if bearerToken == "" {
		return nil, listAPIKeysOutput{}, fmt.Errorf("missing user bearer token")
	}

	keys, err := downstream.CallOp(ctx, rc.deps.downstreamClients().Identity, "list_api_keys", func(ctx context.Context) ([]clients.APIKey, error) {
		return rc.deps.Identity.ListAPIKeys(ctx, bearerToken)
	})
	if err != nil {
		if errors.Is(err, resilience.ErrCircuitOpen) {
			return nil, listAPIKeysOutput{}, fmt.Errorf("identity service unreachable (circuit breaker open)")
		}
		return nil, listAPIKeysOutput{}, fmt.Errorf("list api keys failed: %w", err)
	}

	items := make([]apiKeyListItem, 0, len(keys))
	for _, key := range keys {
		items = append(items, apiKeyListItem{
			CreatedAt:  key.CreatedAt.UTC().Format(time.RFC3339),
			ExpiresAt:  formatOptionalTime(key.ExpiresAt),
			KeyID:      key.ID,
			LastUsedAt: formatOptionalTime(key.LastUsedAt),
			Name:       key.Name,
			Prefix:     key.Prefix,
			Scopes:     append([]string(nil), key.Scopes...),
		})
	}

	return nil, listAPIKeysOutput{Keys: items}, nil
}

func formatOptionalTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
