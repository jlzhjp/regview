//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"
	"go.podman.io/storage/pkg/reexec"
	"go.podman.io/storage/pkg/unshare"
	"regview/internal/app"
	"regview/internal/backend/podman"
	"regview/internal/config"
	"regview/internal/rootless"
)

func main() {
	if reexec.Init() {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := fang.Execute(ctx, command()); err != nil {
		os.Exit(1)
	}
}

func command() *cobra.Command {
	var configFile string
	var dryRun bool
	cmd := &cobra.Command{Use: "regview", Short: "Expose local Docker and Podman images as a read-only registry", Args: cobra.NoArgs}
	config.Flags(cmd.Flags(), config.Defaults(unshare.GetRootlessUID()))
	cmd.Flags().StringVar(&configFile, "config", "", "Configuration file (default: user/system regview.yaml)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and print resolved settings without opening stores or listeners")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(cmd.Flags(), configFile, unshare.GetRootlessUID())
		if err != nil {
			return err
		}
		if dryRun {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(cfg)
		}
		logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
		// Resolve the user's store before re-exec. UID 0 inside the namespace
		// must never select the host's system store. Discovery must also work
		// when Podman or user namespaces are unavailable on a Docker-only host.
		if os.Geteuid() != 0 && (cfg.AutoBackends() || slices.Contains(cfg.Backends, "podman")) {
			opts, setupErr := podman.ResolveOptions(podman.Options{GraphRoot: cfg.PodmanGraphRoot, RunRoot: cfg.PodmanRunRoot, Driver: cfg.PodmanDriver})
			if setupErr == nil && cfg.AutoBackends() {
				setupErr = podman.ExistingStore(opts)
			}
			if setupErr == nil {
				started, err := rootless.Run(cmd.Context(), os.Args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
				if started {
					return err
				}
				setupErr = err
			}
			if !cfg.AutoBackends() {
				return fmt.Errorf("initialize podman: %w", setupErr)
			}
			logger.Warn("skip unavailable local backend", "backend", "podman", "error", setupErr)
			cfg.Backends = []string{"docker"}
		}
		return app.Run(cmd.Context(), cfg, logger)
	}
	return cmd
}
