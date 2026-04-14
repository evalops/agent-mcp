package http

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/evalops/agent-mcp/internal/agentmcp"
	"github.com/evalops/agent-mcp/internal/clients"
	"github.com/evalops/agent-mcp/internal/config"
	"github.com/evalops/service-runtime/health"
	"github.com/evalops/service-runtime/httpkit"
	"github.com/evalops/service-runtime/mtls"
	"github.com/evalops/service-runtime/natsbus"
	"github.com/evalops/service-runtime/redisutil"
	"github.com/evalops/service-runtime/startup"
)

// BuildResult contains the HTTP handler and cleanup resources.
type BuildResult struct {
	Handler http.Handler
	Cleanup func(context.Context)
	Deps    *agentmcp.Deps
}

func BuildRouter(ctx context.Context, cfg config.Config, logger *slog.Logger) (*BuildResult, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Build HTTP clients for downstream services.
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

	// Session store: Redis or in-memory.
	var sessionStore agentmcp.SessionBackend
	if cfg.Session.Store == "redis" && cfg.Session.RedisURL != "" {
		redisClient, err := redisutil.Open(ctx, cfg.Session.RedisURL, redisutil.Options{
			Retry: startup.Config{MaxAttempts: cfg.StartupRetry.MaxAttempts, Delay: cfg.StartupRetry.Delay},
		})
		if err != nil {
			return nil, fmt.Errorf("open redis for sessions: %w", err)
		}
		sessionStore = agentmcp.NewRedisSessionStore(redisClient, time.Hour)
		logger.Info("session store: redis")
	} else {
		sessionStore = agentmcp.NewMemorySessionStore()
		logger.Info("session store: memory")
	}

	eventPublisher := agentmcp.EventPublisher(agentmcp.NoopEventPublisher{})
	if cfg.NATS.URL != "" {
		busPublisher, err := natsbus.Connect(ctx, cfg.NATS.URL, cfg.NATS.Stream, cfg.NATS.Subject, logger)
		if err != nil {
			_ = sessionStore.Close()
			return nil, fmt.Errorf("connect nats publisher: %w", err)
		}
		eventPublisher = agentmcp.NewNATSEventPublisher(busPublisher, logger, busPublisher.Close)
		logger.Info("event publisher: nats", "stream", cfg.NATS.Stream, "subject_prefix", cfg.NATS.Subject)
	} else {
		logger.Info("event publisher: noop")
	}

	deps := &agentmcp.Deps{
		Identity: identityClient,
		Config:   cfg,
		Sessions: sessionStore,
		Metrics:  agentmcp.NewMetrics(),
		Events:   eventPublisher,
		Logger:   logger,
		Breakers: agentmcp.NewBreakers(cfg.Breaker),
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

	// Start session expiry reaper (no-op for Redis which uses TTL).
	stopReaper := agentmcp.RunExpiryReaper(sessionStore, cfg.SessionReapInterval, logger)

	// Health checker: verify all configured downstream services.
	checker := health.New()
	addHTTPHealthCheck(checker, "identity", cfg.Identity.BaseURL, identityHTTP)
	if cfg.Governance.BaseURL != "" {
		addHTTPHealthCheck(checker, "governance", cfg.Governance.BaseURL, governanceHTTP)
	}
	if cfg.Approvals.BaseURL != "" {
		addHTTPHealthCheck(checker, "approvals", cfg.Approvals.BaseURL, approvalsHTTP)
	}
	if cfg.Registry.BaseURL != "" {
		addHTTPHealthCheck(checker, "registry", cfg.Registry.BaseURL, registryHTTP)
	}
	if cfg.Meter.BaseURL != "" {
		addHTTPHealthCheck(checker, "meter", cfg.Meter.BaseURL, meterHTTP)
	}
	if cfg.Memory.BaseURL != "" {
		addHTTPHealthCheck(checker, "memory", cfg.Memory.BaseURL, memoryHTTP)
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", httpkit.HealthHandler(cfg.ServiceName))
	mux.Handle("/readyz", checker.Handler(5*time.Second))
	mux.Handle("/metrics", httpkit.MetricsHandler())
	mux.Handle("/mcp", agentmcp.NewHandler(deps))

	handler := httpkit.WithRequestID(httpkit.WithRequestLogging(logger)(mux))

	cleanup := func(ctx context.Context) {
		stopReaper()
		// Redis-backed sessions are shared across replicas, so only local in-memory
		// sessions should be deregistered during shutdown.
		if _, sharedStore := sessionStore.(*agentmcp.RedisSessionStore); !sharedStore {
			sessions := sessionStore.All()
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
		}
		if err := sessionStore.Close(); err != nil {
			logger.Warn("session store close failed", "error", err)
		}
		deps.Events.Close()
	}

	return &BuildResult{Handler: handler, Cleanup: cleanup, Deps: deps}, nil
}

// addHTTPHealthCheck adds a health check that GETs /healthz on the given base URL.
func addHTTPHealthCheck(checker *health.Checker, name, baseURL string, httpClient *http.Client) {
	if httpClient == nil {
		return
	}
	healthURL, err := url.JoinPath(baseURL, "healthz")
	if err != nil {
		return
	}
	checker.Add(name, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("healthz returned %s", resp.Status)
		}
		return nil
	})
}
