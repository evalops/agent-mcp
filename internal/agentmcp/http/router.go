package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/evalops/agent-mcp/internal/agentmcp/agentmcp"
	"github.com/evalops/agent-mcp/internal/agentmcp/clients"
	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	"github.com/evalops/service-runtime/health"
	"github.com/evalops/service-runtime/httpkit"
	"github.com/evalops/service-runtime/mtls"
	"github.com/evalops/service-runtime/natsbus"
	"github.com/evalops/service-runtime/ratelimit"
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
		return nil, fmt.Errorf("build agent-registry http client: %w", err)
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

	identityClient := clients.NewIdentityClient(cfg.Identity.BaseURL, identityHTTP, cfg.Identity.RequestTimeout).
		WithIntrospectURL(cfg.Identity.IntrospectURL)

	// Session store: Redis for hosted deployments; memory requires explicit local/test opt-in.
	var sessionStore agentmcp.SessionBackend
	switch cfg.Session.Store {
	case "redis":
		redisClient, err := redisutil.Open(ctx, cfg.Session.RedisURL, redisutil.Options{
			Retry: startup.Config{MaxAttempts: cfg.StartupRetry.MaxAttempts, Delay: cfg.StartupRetry.Delay},
		})
		if err != nil {
			return nil, fmt.Errorf("open redis for sessions: %w", err)
		}
		sessionStore = agentmcp.NewRedisSessionStore(redisClient, time.Hour)
		logger.Info("session store: redis")
	case "memory":
		sessionStore = agentmcp.NewMemorySessionStore()
		logger.Info("session store: memory")
	default:
		return nil, fmt.Errorf("unsupported session store %q", cfg.Session.Store)
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
		Identity:   identityClient,
		Config:     cfg,
		Sessions:   sessionStore,
		HabitCache: agentmcp.NewApprovalHabitsCache(),
		Metrics:    agentmcp.NewMetrics(),
		Events:     eventPublisher,
		Logger:     logger,
		Breakers:   agentmcp.NewBreakers(cfg.Breaker),
		Async:      agentmcp.NewAsyncRunner(cfg.BackgroundMaxTasks, logger),
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
	stopHabitSubscriber := func(context.Context) error { return nil }
	if cfg.NATS.URL != "" {
		stop, err := agentmcp.StartApprovalHabitSubscriber(
			ctx,
			cfg.NATS.URL,
			cfg.Approvals.EventStream,
			cfg.Approvals.EventSubject,
			cfg.Approvals.EventDurable,
			deps.HabitCache,
			logger.With("subscriber", "approval_habits"),
		)
		if err != nil {
			_ = sessionStore.Close()
			deps.Events.Close()
			return nil, fmt.Errorf("start approval habit subscriber: %w", err)
		}
		stopHabitSubscriber = stop
	}

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
		addHTTPHealthCheck(checker, "agent-registry", cfg.Registry.BaseURL, registryHTTP)
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
	mux.Handle("/.well-known/oauth-protected-resource", newProtectedResourceMetadataHandler(cfg))
	mcpLimiter := ratelimit.New(ratelimit.Config{
		RequestsPerSecond: cfg.MCPRateLimit.RequestsPerSecond,
		Burst:             cfg.MCPRateLimit.Burst,
		ExemptPaths:       map[string]bool{},
		KeyFunc:           mcpRateLimitKey,
		ScopeFunc: func(_ *http.Request) string {
			return "/mcp"
		},
		ServiceName: cfg.ServiceName,
		OnLimited: func(r *http.Request) {
			logger.Warn("mcp request rate limited", "client", clientAddress(r))
		},
	})
	mcpHandler := newMCPAuthMiddleware(cfg, identityClient, sessionStore, logger)(agentmcp.NewHandler(deps))
	mux.Handle("/mcp", mcpLimiter.Middleware(mcpHandler))

	maxBodyBytes := cfg.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1 << 20
	}
	handler := httpkit.WithRequestID(httpkit.WithRequestLogging(logger)(httpkit.WithMaxBodySize(maxBodyBytes)(mux)))

	cleanup := func(ctx context.Context) {
		mcpLimiter.Close()
		stopReaper()
		if err := stopHabitSubscriber(ctx); err != nil {
			logger.Warn("approval habit subscriber close failed", "error", err)
		}
		// Deregister sessions owned by this instance. For Redis-backed stores,
		// only deregister sessions this process created (LocalSessions), not
		// every session across all replicas. For in-memory stores, All() is
		// equivalent since all sessions are local.
		var sessions map[string]*agentmcp.SessionState
		if redisStore, ok := sessionStore.(*agentmcp.RedisSessionStore); ok {
			sessions = redisStore.LocalSessions()
		} else {
			sessions = sessionStore.All()
		}
		if len(sessions) > 0 {
			logger.Info("graceful shutdown: deregistering active sessions", "count", len(sessions))
			for _, state := range sessions {
				if state == nil || state.IsAnonymous() || state.AgentToken == "" {
					continue
				}
				if err := identityClient.DeregisterAgent(ctx, state.AgentToken); err != nil {
					logger.Warn("shutdown deregister failed", "agent_id", state.AgentID, "error", err)
				} else {
					logger.Info("shutdown deregistered agent", "agent_id", state.AgentID)
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

func mcpRateLimitKey(r *http.Request) string {
	if token := bearerTokenFromHeader(r.Header.Get("Authorization")); token != "" {
		sum := sha256.Sum256([]byte(token))
		return "bearer:" + hex.EncodeToString(sum[:])[:16]
	}
	return "ip:" + clientAddress(r)
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
		defer func() {
			_ = resp.Body.Close()
		}()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("healthz returned %s", resp.Status)
		}
		return nil
	})
}
