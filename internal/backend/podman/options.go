package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.podman.io/storage"
	storageTypes "go.podman.io/storage/types"
	"regview/internal/backend/podman/storageconfig"
)

// ResolveOptions resolves configuration without opening an image store. Rootless
// inheritance and explicit options are resolved before applying regview overrides.
func ResolveOptions(overrides Options) (storage.StoreOptions, error) {
	return resolveOptions(overrides, storageTypes.LoadOptions{})
}

func resolveOptions(overrides Options, load storageTypes.LoadOptions) (storage.StoreOptions, error) {
	resolver := overrides.Resolver
	if resolver == nil {
		resolver = storageconfig.Native
	}
	opts, err := resolver(load)
	if err != nil {
		return opts, fmt.Errorf("resolve Podman storage: %w", err)
	}
	if overrides.GraphRoot != "" {
		opts.GraphRoot = overrides.GraphRoot
	}
	if overrides.RunRoot != "" {
		opts.RunRoot = overrides.RunRoot
	}
	if overrides.Driver != "" {
		if overrides.Driver != opts.GraphDriverName {
			opts.GraphDriverOptions = nil
		}
		opts.GraphDriverName = overrides.Driver
	}
	if opts.ImageStore != "" && opts.ImageStore == opts.GraphRoot {
		return opts, fmt.Errorf("Podman imagestore must differ from graphroot")
	}
	for _, path := range []string{opts.GraphRoot, opts.RunRoot, opts.ImageStore} {
		if path != "" && !filepath.IsAbs(path) {
			return opts, fmt.Errorf("Podman storage path must be absolute: %q", path)
		}
	}
	return opts, nil
}

// ExistingStore prevents discovery from creating a new, empty image store.
// An initialized image directory qualifies even if it currently has no images.
// GetStore and List still have to succeed before the backend is enabled.
func ExistingStore(opts storage.StoreOptions) error {
	root := opts.GraphRoot
	if opts.ImageStore != "" {
		root = opts.ImageStore
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect existing Podman store %s: %w", root, err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), "-images") && (opts.GraphDriverName == "" || entry.Name() == opts.GraphDriverName+"-images") {
			return nil
		}
	}
	return fmt.Errorf("no initialized Podman image store at %s", root)
}
