package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/evalops/agent-mcp/internal/config"
	httpapi "github.com/evalops/agent-mcp/internal/http"
	"github.com/evalops/service-runtime/mtls"
)

func main() {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handler, err := httpapi.BuildRouter(cfg)
	if err != nil {
		log.Fatalf("build router: %v", err)
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverTLSConfig, err := mtls.BuildServerTLSConfig(cfg.TLS)
	if err != nil {
		log.Fatalf("configure tls: %v", err)
	}
	server.TLSConfig = serverTLSConfig

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("starting %s on %s", cfg.ServiceName, cfg.Addr)
	if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
		err = server.ListenAndServeTLS("", "")
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen failed: %v", err)
	}
}
