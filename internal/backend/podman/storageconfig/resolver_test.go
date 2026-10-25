package storageconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	storageTypes "go.podman.io/storage/types"
)

func TestResolverSelectionAndInheritance(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", data)
	for _, name := range []string{"CONTAINERS_STORAGE_CONF", "CONTAINERS_STORAGE_CONF_OVERRIDE", "STORAGE_DRIVER", "STORAGE_OPTS"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "usr/share/containers/storage.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[storage]
driver = "overlay"
graphroot = "/system/graph"
runroot = "/system/run"
[storage.options]
additionalimagestores = ["/system/shared"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, major := range []int{4, 5, 6, 7} {
		selected := discover(t.Context(), 1234, func(context.Context) (version, error) {
			return version{major: major, text: "test version"}, nil
		})
		opts, err := selected.Resolve(storageTypes.LoadOptions{RootForImplicitAbsolutePaths: root})
		if err != nil {
			t.Fatal(err)
		}
		if major == 5 {
			if selected.Name != "podman5" || opts.GraphRoot != filepath.Join(data, "containers/storage") || len(opts.GraphDriverOptions) != 0 {
				t.Fatalf("Podman 5 compatibility not selected: %+v, %+v", selected, opts)
			}
		} else if selected.Name != "native" || opts.GraphRoot != "/system/graph" || !slices.Contains(opts.GraphDriverOptions, "imagestore=/system/shared") {
			t.Fatalf("major %d used compatibility rules: %+v, %+v", major, selected, opts)
		}
		if selected.Warning != nil || selected.Version != "test version" {
			t.Fatalf("selection metadata: %+v", selected)
		}
	}
}

func TestRootfulSelectionDoesNotProbeVersion(t *testing.T) {
	selected := discover(t.Context(), 0, func(context.Context) (version, error) {
		t.Fatal("rootful resolution does not need a version probe")
		return version{}, nil
	})
	if selected.Name != "native" || selected.Warning != nil || selected.Resolve == nil {
		t.Fatalf("rootful selection: %+v", selected)
	}
}

func TestUnknownVersionUsesNativeWithDiagnostic(t *testing.T) {
	failure := errors.New("cannot identify local Podman")
	selected := discover(t.Context(), 1234, func(context.Context) (version, error) {
		return version{}, failure
	})
	if selected.Name != "native" || !errors.Is(selected.Warning, failure) || selected.Resolve == nil {
		t.Fatalf("unknown version selected compatibility: %+v", selected)
	}
}
