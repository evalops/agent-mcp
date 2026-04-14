package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/evalops/agent-mcp/internal/config"
	httpapi "github.com/evalops/agent-mcp/internal/http"
	"github.com/evalops/service-runtime/mtls"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result.Cleanup(shutdownCtx)
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("starting service", "service", cfg.ServiceName, "addr", cfg.Addr, "session_store", cfg.Session.Store)
	if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
		err = server.ListenAndServeTLS("", "")
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		logger.Error("listen failed", "error", err)
		os.Exit(1)
	}
}
