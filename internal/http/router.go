package http

import (
	"context"
	"net/http"

	"github.com/evalops/agent-mcp/internal/agentmcp"
	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
	"github.com/evalops/service-runtime/httpkit"
	"github.com/evalops/service-runtime/mtls"
)

func BuildRouter(cfg config.Config) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	identityHTTP, err := mtls.BuildHTTPClient(cfg.Identity.TLS)
	if err != nil {
		return nil, err
	}
	registryHTTP, err := mtls.BuildHTTPClient(cfg.Registry.TLS)
	if err != nil {
		return nil, err
	}
	governanceHTTP, err := mtls.BuildHTTPClient(cfg.Governance.TLS)
	if err != nil {
		return nil, err
	}
	approvalsHTTP, err := mtls.BuildHTTPClient(cfg.Approvals.TLS)
	if err != nil {
		return nil, err
	}
	meterHTTP, err := mtls.BuildHTTPClient(cfg.Meter.TLS)
	if err != nil {
		return nil, err
	}
	memoryHTTP, err := mtls.BuildHTTPClient(cfg.Memory.TLS)
	if err != nil {
		return nil, err
	}

	deps := &agentmcp.Deps{
		Identity: clients.NewIdentityClient(cfg.Identity.BaseURL, identityHTTP, cfg.Identity.RequestTimeout),
		Config:   cfg,
		Sessions: agentmcp.NewSessionStore(),
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

	mux := http.NewServeMux()
	mux.Handle("/healthz", httpkit.HealthHandler(cfg.ServiceName))
	mux.Handle("/readyz", httpkit.ReadyHandler(func(_ context.Context) error { return nil }))
	mux.Handle("/metrics", httpkit.MetricsHandler())
	mux.Handle("/mcp", agentmcp.NewHandler(deps))

	handler := httpkit.WithRequestID(httpkit.WithRequestLogging(nil)(mux))
	return handler, nil
}
