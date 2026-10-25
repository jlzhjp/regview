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
	"regview/internal/assets"
	"regview/internal/backend/podman"
	"regview/internal/backend/podman/storageconfig"
	"regview/internal/config"
	"regview/internal/rootless"
)

func main() {
	if reexec.Init() {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := fang.Execute(ctx, command())
	stop()
	if err != nil {
		os.Exit(1)
	}
}

func command() *cobra.Command {
	var configFile string
	var dryRun bool
	cmd := &cobra.Command{Use: "regview", Short: "Expose local Docker and Podman images as a read-only registry", Args: cobra.NoArgs}
	cmd.AddCommand(&cobra.Command{
		Use:   "licenses",
		Short: "View third-party license information",
		Long:  "View license information for Go and npm production dependencies included in this build of regview.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return assets.WriteLicenses(cmd.OutOrStdout())
		},
	})
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
		selected := storageconfig.Selection{Name: "native", Resolve: storageconfig.Native}
		if cfg.AutoBackends() || slices.Contains(cfg.Backends, "podman") {
			selected = storageconfig.Discover(cmd.Context(), unshare.GetRootlessUID())
			if selected.Warning != nil {
				logger.Warn("cannot identify local Podman version; using native storage resolver", "error", selected.Warning)
			}
		}
		// Resolve the user's store before re-exec. UID 0 inside the namespace
		// must never select the host's system store. Discovery must also work
		// when Podman or user namespaces are unavailable on a Docker-only host.
		if os.Geteuid() != 0 && (cfg.AutoBackends() || slices.Contains(cfg.Backends, "podman")) {
			opts, setupErr := podman.ResolveOptions(podman.Options{GraphRoot: cfg.PodmanGraphRoot, RunRoot: cfg.PodmanRunRoot, Driver: cfg.PodmanDriver, Resolver: selected.Resolve})
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
		if cfg.AutoBackends() || slices.Contains(cfg.Backends, "podman") {
			logger.Info("selected Podman storage resolver", "resolver", selected.Name, "version", selected.Version)
		}
		return app.Run(cmd.Context(), cfg, logger, selected.Resolve)
	}
	return cmd
}
