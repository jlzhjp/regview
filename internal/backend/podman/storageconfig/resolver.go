// Package storageconfig selects and supplies Podman storage option resolvers.
package storageconfig

import (
	"context"

	"go.podman.io/storage"
	storageTypes "go.podman.io/storage/types"
	"regview/internal/backend/podman/storageconfig/podman5"
)

// Resolver loads configuration only; it never opens an image store. Callers
// apply their own explicit overrides after resolution.
type Resolver func(storageTypes.LoadOptions) (storage.StoreOptions, error)

func Native(load storageTypes.LoadOptions) (storage.StoreOptions, error) {
	return storageTypes.LoadStoreOptions(load)
}

type Selection struct {
	Name, Version string
	Resolve       Resolver
	Warning       error
}

// Discover enables compatibility only for a positively identified Podman 5
// rootless installation. An absent or unidentifiable client uses native rules.
func Discover(ctx context.Context, uid int) Selection {
	return discover(ctx, uid, localVersion)
}

func discover(ctx context.Context, uid int, detect func(context.Context) (version, error)) Selection {
	selected := Selection{Name: "native", Resolve: Native}
	if uid == 0 {
		return selected
	}
	v, err := detect(ctx)
	if err != nil {
		selected.Warning = err
		return selected
	}
	selected.Version = v.text
	if v.major == 5 {
		selected.Name = "podman5"
		selected.Resolve = func(load storageTypes.LoadOptions) (storage.StoreOptions, error) {
			return podman5.Resolve(uid, load)
		}
	}
	return selected
}
