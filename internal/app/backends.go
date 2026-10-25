package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"regview/internal/backend/docker"
	"regview/internal/backend/podman"
	"regview/internal/backend/podman/storageconfig"
	"regview/internal/config"
	"regview/internal/image"
)

type backendOpener func(context.Context, string) (image.Backend, error)

func localOpener(cfg config.Config, logger *slog.Logger, resolver storageconfig.Resolver) backendOpener {
	return func(ctx context.Context, name string) (image.Backend, error) {
		switch name {
		case "docker":
			return docker.New(ctx, cfg.DockerEngineSocket, cfg.DockerSocket, cfg.DockerNamespace)
		case "podman":
			return podman.New(podman.Options{
				GraphRoot: cfg.PodmanGraphRoot, RunRoot: cfg.PodmanRunRoot, Driver: cfg.PodmanDriver,
				Resolver:     resolver,
				ExistingOnly: cfg.AutoBackends(),
				BlobCache:    podman.BlobCacheOptions{TTL: cfg.PodmanBlobCacheTTL, MaxBytes: cfg.PodmanBlobCacheMaxBytes, MaxEntries: cfg.PodmanBlobCacheMaxEntries},
				OnCacheError: func(err error) { logger.Warn("dispose Podman blob cache entry", "error", err) },
			})
		default:
			return nil, fmt.Errorf("unknown backend %q", name)
		}
	}
}

// openBackends owns partial-startup cleanup. Discovery only changes how failures
// are handled; successful connections have the same lifetime in either mode.
func openBackends(ctx context.Context, names []string, open backendOpener, logger *slog.Logger) ([]image.Backend, error) {
	auto := names == nil
	if auto {
		names = []string{"docker", "podman"}
	}
	var backends []image.Backend
	var failures error
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			closeBackends(backends, logger)
			return nil, err
		}
		probe, cancel := context.WithTimeout(ctx, 15*time.Second)
		backend, err := open(probe, name)
		if err == nil {
			// A reachable daemon or an opened driver alone doesn't guarantee
			// that its image metadata is readable. Retain no probe inventory.
			_, err = backend.List(probe)
			if err != nil {
				closeBackends([]image.Backend{backend}, logger)
			}
		}
		cancel()
		if err != nil {
			failure := fmt.Errorf("initialize %s: %w", name, err)
			if !auto {
				closeBackends(backends, logger)
				return nil, failure
			}
			failures = errors.Join(failures, failure)
			logger.Warn("skip unavailable local backend", "backend", name, "error", err)
			continue
		}
		logger.Info("enabled local backend", "backend", name)
		backends = append(backends, backend)
	}
	if err := ctx.Err(); err != nil {
		closeBackends(backends, logger)
		return nil, err
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("no usable local backends: %w", failures)
	}
	return backends, nil
}

func closeBackends(backends []image.Backend, logger *slog.Logger) {
	for _, backend := range backends {
		if err := backend.Close(); err != nil {
			logger.Error("close backend", "backend", backend.Name(), "error", err)
		}
	}
}
