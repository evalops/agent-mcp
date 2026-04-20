package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/evalops/agent-mcp/internal/agentmcp/config"
	httpapi "github.com/evalops/agent-mcp/internal/agentmcp/http"
	"github.com/evalops/service-runtime/mtls"
	"github.com/evalops/service-runtime/startup"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := config.Load()
	ctx, stop := startup.NotifyContext()
	defer stop()

	result, err := httpapi.BuildRouter(ctx, cfg, logger)
	if err != nil {
		logger.Error("build router failed", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           result.Handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverTLSConfig, err := mtls.BuildServerTLSConfig(cfg.TLS)
	if err != nil {
		logger.Error("configure tls failed", "error", err)
		os.Exit(1)
	}
	server.TLSConfig = serverTLSConfig

	lifecycle := startup.NewLifecycle()
	lifecycle.OnShutdown("router cleanup", func(ctx context.Context) error {
		result.Cleanup(ctx)
		return nil
	})

	logger = logger.With("session_store", cfg.Session.Store)
	if err := startup.RunHTTPServer(ctx, startup.HTTPServerConfig{
		ServiceName:     cfg.ServiceName,
		Addr:            cfg.Addr,
		Version:         cfg.Version,
		Server:          server,
		ShutdownTimeout: cfg.ShutdownTimeout,
		Lifecycle:       lifecycle,
		Logger:          logger,
		TLSCertFile:     cfg.TLS.CertFile,
		TLSKeyFile:      cfg.TLS.KeyFile,
		TLSClientCAFile: cfg.TLS.ClientCAFile,
	}); err != nil {
		logger.Error("agent-mcp exited", "error", err)
		os.Exit(1)
	}
}
