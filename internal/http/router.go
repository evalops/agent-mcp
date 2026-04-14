package http

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/evalops/agent-mcp/internal/agentmcp"
	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
	"github.com/evalops/service-runtime/httpkit"
	"github.com/evalops/service-runtime/mtls"
)

// BuildResult contains the HTTP handler and cleanup resources.
type BuildResult struct {
	Handler http.Handler
	Cleanup func(context.Context)
	Deps    *agentmcp.Deps
}

func BuildRouter(cfg config.Config, logger *slog.Logger) (*BuildResult, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	identityHTTP, err := mtls.BuildHTTPClient(cfg.Identity.TLS)
	if err != nil {
		return nil, fmt.Errorf("build identity http client: %w", err)
	}
	registryHTTP, err := mtls.BuildHTTPClient(cfg.Registry.TLS)
	if err != nil {
		return nil, fmt.Errorf("build registry http client: %w", err)
	}
	governanceHTTP, err := mtls.BuildHTTPClient(cfg.Governance.TLS)
	if err != nil {
		return nil, fmt.Errorf("build governance http client: %w", err)
	}
	approvalsHTTP, err := mtls.BuildHTTPClient(cfg.Approvals.TLS)
	if err != nil {
		return nil, fmt.Errorf("build approvals http client: %w", err)
	}
	meterHTTP, err := mtls.BuildHTTPClient(cfg.Meter.TLS)
	if err != nil {
		return nil, fmt.Errorf("build meter http client: %w", err)
	}
	memoryHTTP, err := mtls.BuildHTTPClient(cfg.Memory.TLS)
	if err != nil {
		return nil, fmt.Errorf("build memory http client: %w", err)
	}

	identityClient := clients.NewIdentityClient(cfg.Identity.BaseURL, identityHTTP, cfg.Identity.RequestTimeout)

	deps := &agentmcp.Deps{
		Identity: identityClient,
		Config:   cfg,
		Sessions: agentmcp.NewSessionStore(),
		Metrics:  agentmcp.NewMetrics(),
		Events:   agentmcp.NoopEventPublisher{},
		Logger:   logger,
	}

	if cfg.Registry.BaseURL != "" {
		deps.Registry = clients.NewRegistryClient(cfg.Registry.BaseURL, registryHTTP)
	}
	if cfg.Governance.BaseURL != "" {
		deps.Governance = clients.NewGovernanceClient(cfg.Governance.BaseURL, governanceHTTP)
	}
	if cfg.Approvals.BaseURL != "" {
		deps.Approvals = clients.NewApprovalsClient(cfg.Approvals.BaseURL, approvalsHTTP)
	}
	if cfg.Meter.BaseURL != "" {
		deps.Meter = clients.NewMeterClient(cfg.Meter.BaseURL, meterHTTP)
	}
	if cfg.Memory.BaseURL != "" {
		deps.Memory = clients.NewMemoryClient(cfg.Memory.BaseURL, memoryHTTP)
	}

	// Start session expiry reaper.
	stopReaper := agentmcp.RunExpiryReaper(deps.Sessions, cfg.SessionReapInterval, logger)

	// Health check: verify identity is reachable.
	readyCheck := func(ctx context.Context) error {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, cfg.Identity.BaseURL+"/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := identityHTTP.Do(req)
		if err != nil {
			return fmt.Errorf("identity unreachable: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			return fmt.Errorf("identity unhealthy: %d", resp.StatusCode)
		}
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", httpkit.HealthHandler(cfg.ServiceName))
	mux.Handle("/readyz", httpkit.ReadyHandler(readyCheck))
	mux.Handle("/metrics", httpkit.MetricsHandler())
	mux.Handle("/mcp", agentmcp.NewHandler(deps))

	handler := httpkit.WithRequestID(httpkit.WithRequestLogging(logger)(mux))

	cleanup := func(ctx context.Context) {
		stopReaper()
		// Graceful shutdown: deregister all active sessions.
		sessions := deps.Sessions.All()
		if len(sessions) > 0 {
			logger.Info("graceful shutdown: deregistering active sessions", "count", len(sessions))
			for _, state := range sessions {
				if err := identityClient.DeregisterAgent(ctx, state.AgentToken); err != nil {
					logger.Warn("shutdown deregister failed", "agent_id", state.AgentID, "error", err)
				} else {
					logger.Info("shutdown deregistered agent", "agent_id", state.AgentID)
				}
			}
		}
		deps.Events.Close()
	}

	return &BuildResult{Handler: handler, Cleanup: cleanup, Deps: deps}, nil
}
