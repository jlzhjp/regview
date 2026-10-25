package podman5

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"go.podman.io/storage/pkg/homedir"
	"go.podman.io/storage/pkg/unshare"
	storageTypes "go.podman.io/storage/types"
)

func TestRootlessStorageResolution(t *testing.T) {
	if unshare.GetRootlessUID() == 0 {
		t.Skip("rootless integration of official configuration loader")
	}
	root, userConfig, data := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", userConfig)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("CONTAINERS_STORAGE_CONF", "")
	t.Setenv("CONTAINERS_STORAGE_CONF_OVERRIDE", "")
	t.Setenv("STORAGE_DRIVER", "")
	t.Setenv("STORAGE_OPTS", "")
	if err := os.Unsetenv("STORAGE_OPTS"); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "usr/share/containers/storage.conf")
	writeStorageConfig(t, main, `[storage]
driver = "overlay"
graphroot = "/system/graph"
runroot = "/system/run"
imagestore = "/shared/images"
[storage.options]
additionalimagestores = ["/shared/extra"]
[storage.options.overlay]
mountopt = "nodev"
`)
	load := storageTypes.LoadOptions{RootForImplicitAbsolutePaths: root}
	opts, err := Resolve(unshare.GetRootlessUID(), load)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := homedir.GetRuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	if opts.GraphRoot != filepath.Join(data, "containers/storage") || opts.RunRoot != filepath.Join(runtime, "containers") {
		t.Fatalf("system roots leaked into rootless options: %+v", opts)
	}
	if opts.ImageStore != "" || len(opts.GraphDriverOptions) != 0 {
		t.Fatalf("rootful stores or mount options leaked into rootless options: %+v", opts)
	}
	if _, err := os.Stat(opts.GraphRoot); !os.IsNotExist(err) {
		t.Fatalf("resolution created graphroot: %v", err)
	}
	// System rootless-specific drop-ins apply, while generic system roots do not.
	writeStorageConfig(t, filepath.Join(root, "etc/containers/storage.rootless.conf.d/10-paths.conf"), "[storage]\ngraphroot = \"$XDG_DATA_HOME/rootless-$UID\"\nrunroot = \"/custom/runtime\"\n")
	opts, err = Resolve(unshare.GetRootlessUID(), load)
	if err != nil {
		t.Fatal(err)
	}
	if opts.GraphRoot != filepath.Join(data, "rootless-"+strconv.Itoa(unshare.GetRootlessUID())) || opts.RunRoot != "/custom/runtime" {
		t.Fatalf("rootless drop-in not applied: %+v", opts)
	}
	writeStorageConfig(t, filepath.Join(userConfig, "containers/storage.conf.d/99-user.conf"), "[storage]\ngraphroot = \"/user/graph\"\nrunroot = \"/user/run\"\n")
	opts, err = Resolve(unshare.GetRootlessUID(), load)
	if err != nil || opts.GraphRoot != "/user/graph" || opts.RunRoot != "/user/run" {
		t.Fatalf("user drop-in: %+v, %v", opts, err)
	}
	// An explicitly selected config is authoritative, even with system-looking paths.
	selected := filepath.Join(t.TempDir(), "storage.conf")
	writeStorageConfig(t, selected, "[storage]\ndriver = \"overlay\"\ngraphroot = \"/selected/graph\"\nrunroot = \"/selected/run\"\nimagestore = \"/selected/images\"\n[storage.options]\nadditionalimagestores = [\"/selected/extra\"]\n[storage.options.overlay]\nmountopt = \"nodev\"\n")
	t.Setenv("CONTAINERS_STORAGE_CONF", selected)
	opts, err = Resolve(unshare.GetRootlessUID(), load)
	if err != nil || opts.GraphRoot != "/selected/graph" || opts.RunRoot != "/selected/run" {
		t.Fatalf("explicit storage config: %+v, %v", opts, err)
	}
	if opts.ImageStore != "/selected/images" || !slices.Contains(opts.GraphDriverOptions, "imagestore=/selected/extra") || !slices.Contains(opts.GraphDriverOptions, "overlay.mountopt=nodev") {
		t.Fatalf("explicit options lost: %+v", opts)
	}
	t.Setenv("CONTAINERS_STORAGE_CONF", filepath.Join(t.TempDir(), "missing.conf"))
	if _, err := Resolve(unshare.GetRootlessUID(), load); err == nil {
		t.Fatal("invalid explicit configuration silently replaced")
	}
}
