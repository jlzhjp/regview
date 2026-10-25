// Package app wires storage, catalog, and the two HTTP listeners together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"regview/internal/admin"
	"regview/internal/catalog"
	"regview/internal/config"
	"regview/internal/registry"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	backends, err := openBackends(ctx, cfg.Backends, localOpener(cfg, logger), logger)
	if err != nil {
		return err
	}
	defer closeBackends(backends, logger)
	catalog := catalog.New(backends, cfg.Hostnames, logger)
	registryListener, err := net.Listen("tcp", cfg.RegistryListen)
	if err != nil {
		return fmt.Errorf("registry listener: %w", err)
	}
	defer registryListener.Close()
	adminListener, err := net.Listen("tcp", cfg.AdminListen)
	if err != nil {
		return fmt.Errorf("admin listener: %w", err)
	}
	defer adminListener.Close()
	servers := []*http.Server{
		server(registry.New(catalog, logger), ctx),
		server(admin.New(catalog, logger), ctx),
	}
	var names []string
	for _, backend := range backends {
		names = append(names, backend.Name())
	}
	logger.Info("serving local images", "registry", registryListener.Addr(), "admin", adminListener.Addr(), "hostnames", cfg.Hostnames, "backends", names)
	var workers sync.WaitGroup
	errorsCh := make(chan error, 2)
	for i, listener := range []net.Listener{registryListener, adminListener} {
		workers.Go(func() { errorsCh <- servers[i].Serve(listener) })
	}
	var serveError error
	select {
	case <-ctx.Done():
	case serveError = <-errorsCh:
	}
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	for _, server := range servers {
		if err := server.Shutdown(shutdown); err != nil {
			logger.Warn("HTTP shutdown", "error", err)
			_ = server.Close()
		}
	}
	workers.Wait()
	if errors.Is(serveError, http.ErrServerClosed) {
		return nil
	}
	return serveError
}

func server(handler http.Handler, ctx context.Context) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20, MaxHeaderValueCount: 100, BaseContext: func(net.Listener) context.Context { return ctx }}
}
