package podman

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	storageTypes "go.podman.io/storage/types"
)

func clearStorageEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"CONTAINERS_STORAGE_CONF", "CONTAINERS_STORAGE_CONF_OVERRIDE", "STORAGE_DRIVER", "STORAGE_OPTS"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPodmanRootlessInheritance(t *testing.T) {
	clearStorageEnvironment(t)
	root, user, data := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", user)
	t.Setenv("XDG_DATA_HOME", data)
	writeStorageConfig(t, filepath.Join(root, "usr/share/containers/storage.conf"), `[storage]
driver = "overlay"
graphroot = "/rootful/graph"
runroot = "/rootful/run"
imagestore = "/rootful/images"
rootless_storage_path = "$XDG_DATA_HOME/per-user-$UID"
transient_store = true
[storage.options]
additionalimagestores = ["/rootful/shared"]
additionallayerstores = ["/rootful/layers"]
root-auto-userns-user = "root"
mount_program = "/rootful/mount-program"
mountopt = "nodev,metacopy=on"
ignore_chown_errors = "true"
[storage.options.pull_options]
enable_partial_images = "true"
`)
	load := storageTypes.LoadOptions{RootForImplicitAbsolutePaths: root}
	opts, err := loadRootlessOptions(1234, load)
	if err != nil {
		t.Fatal(err)
	}
	if opts.GraphRoot != filepath.Join(data, "per-user-1234") || opts.GraphDriverName != "overlay" {
		t.Fatalf("rootless defaults: %+v", opts)
	}
	if opts.ImageStore != "" || opts.TransientStore || opts.RootAutoNsUser != "" || !reflect.DeepEqual(opts.GraphDriverOptions, []string{"ignore_chown_errors=true"}) {
		t.Fatalf("rootful-only settings leaked: %+v", opts)
	}
	if opts.PullOptions["enable_partial_images"] != "true" {
		t.Fatal("lost inherited pull options")
	}
	// A user file replaces the system configuration, including empty tables.
	writeStorageConfig(t, filepath.Join(user, "containers/storage.conf"), "[storage]\n")
	opts, err = loadRootlessOptions(1234, load)
	if err != nil || opts.GraphRoot != filepath.Join(data, "containers/storage") || len(opts.GraphDriverOptions) != 0 || len(opts.PullOptions) != 0 {
		t.Fatalf("user configuration inherited system settings: %+v, %v", opts, err)
	}
	writeStorageConfig(t, filepath.Join(user, "containers/storage.conf"), `[storage]
driver = "overlay"
imagestore = "/user/images"
transient_store = true
[storage.options]
additionalimagestores = ["/user/shared"]
[storage.options.overlay]
mount_program = "/user/mount-program"
mountopt = "nodev"
`)
	opts, err = loadRootlessOptions(1234, load)
	if err != nil || opts.ImageStore != "/user/images" || !opts.TransientStore || !slices.Contains(opts.GraphDriverOptions, "imagestore=/user/shared") || !slices.Contains(opts.GraphDriverOptions, "overlay.mount_program=/user/mount-program") || !slices.Contains(opts.GraphDriverOptions, "overlay.mountopt=nodev") {
		t.Fatalf("explicit rootless options lost: %+v, %v", opts, err)
	}
	// Environment options remain explicit and must not be filtered as system options.
	t.Setenv("STORAGE_DRIVER", "vfs")
	t.Setenv("STORAGE_OPTS", "vfs.ignore_chown_errors=true")
	opts, err = loadRootlessOptions(1234, load)
	if err != nil || opts.GraphDriverName != "vfs" || !reflect.DeepEqual(opts.GraphDriverOptions, []string{"vfs.ignore_chown_errors=true"}) {
		t.Fatalf("environment options: %+v, %v", opts, err)
	}
	t.Setenv("STORAGE_OPTS", "")
	opts, err = loadRootlessOptions(1234, load)
	if err != nil || len(opts.GraphDriverOptions) != 0 {
		t.Fatalf("explicitly empty STORAGE_OPTS: %+v, %v", opts, err)
	}
	// User graphroot wins over the legacy path, and both roots expand $UID.
	writeStorageConfig(t, filepath.Join(user, "containers/storage.conf"), "[storage]\nrootless_storage_path = \"/legacy\"\ngraphroot = \"$XDG_DATA_HOME/graph-$UID\"\nrunroot = \"$XDG_DATA_HOME/run-$UID\"\n")
	opts, err = loadRootlessOptions(1234, load)
	if err != nil || opts.GraphRoot != filepath.Join(data, "graph-1234") || opts.RunRoot != filepath.Join(data, "run-1234") {
		t.Fatalf("explicit roots and legacy precedence: %+v, %v", opts, err)
	}
}

func TestRootlessExistingDriverAndPriority(t *testing.T) {
	clearStorageEnvironment(t)
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", data)
	graph := filepath.Join(data, "containers/storage")
	if err := os.MkdirAll(filepath.Join(graph, "vfs-images"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "usr/share/containers/storage.conf")
	writeStorageConfig(t, path, "[storage]\ndriver = \"zfs\"\n[storage.options]\nadditionalimagestores = [\"/rootful/shared\"]\n")
	load := storageTypes.LoadOptions{RootForImplicitAbsolutePaths: root}
	opts, err := loadRootlessOptions(1234, load)
	if err != nil || opts.GraphDriverName != "vfs" || len(opts.GraphDriverOptions) != 0 {
		t.Fatalf("existing rootless driver: %+v, %v", opts, err)
	}
	writeStorageConfig(t, path, "[storage]\ndriver = \"zfs\"\ndriver_priority = [\"vfs\", \"overlay\"]\n")
	opts, err = loadRootlessOptions(1234, load)
	if err != nil || opts.GraphDriverName != "" || !reflect.DeepEqual(opts.GraphDriverPriority, []string{"vfs", "overlay"}) {
		t.Fatalf("rootless driver priority: %+v, %v", opts, err)
	}
}
