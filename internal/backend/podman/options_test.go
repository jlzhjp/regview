package podman

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.podman.io/storage"
	storageTypes "go.podman.io/storage/types"
)

func TestInjectedResolverAndOverrides(t *testing.T) {
	original := storage.StoreOptions{GraphRoot: "/original/graph", RunRoot: "/original/run", GraphDriverName: "overlay", GraphDriverOptions: []string{"overlay.mountopt=nodev"}}
	calls := 0
	load := storageTypes.LoadOptions{RootForImplicitAbsolutePaths: "/isolated"}
	options := Options{GraphRoot: "/override/graph", RunRoot: "/override/run", Driver: "vfs"}
	options.Resolver = func(got storageTypes.LoadOptions) (storage.StoreOptions, error) {
		calls++
		if got != load {
			t.Fatalf("load options: %+v", got)
		}
		return original, nil
	}
	got, err := resolveOptions(options, load)
	if err != nil || calls != 1 || got.GraphRoot != options.GraphRoot || got.RunRoot != options.RunRoot || got.GraphDriverName != "vfs" || len(got.GraphDriverOptions) != 0 {
		t.Fatalf("overrides: %+v, calls=%d, err=%v", got, calls, err)
	}
	if len(original.GraphDriverOptions) != 1 || original.GraphDriverOptions[0] != "overlay.mountopt=nodev" {
		t.Fatal("mutated resolver-owned options")
	}
	failure := errors.New("resolver failed")
	options.Resolver = func(storageTypes.LoadOptions) (storage.StoreOptions, error) { return storage.StoreOptions{}, failure }
	if _, err := resolveOptions(options, load); !errors.Is(err, failure) {
		t.Fatalf("resolver error: %v", err)
	}
}
func TestExistingStoreDoesNotCreateStorage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	opts := storage.StoreOptions{GraphRoot: root, GraphDriverName: "vfs"}
	if ExistingStore(opts) == nil {
		t.Fatal("missing store accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("discovery created storage")
	}
	if err := os.MkdirAll(filepath.Join(root, "overlay-images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if ExistingStore(opts) == nil {
		t.Fatal("wrong driver accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "vfs-images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ExistingStore(opts); err != nil {
		t.Fatal("empty initialized store rejected:", err)
	}
	opts.ImageStore, opts.GraphRoot = root, "/unused/graph"
	if err := ExistingStore(opts); err != nil {
		t.Fatal("separate image store ignored:", err)
	}
}
