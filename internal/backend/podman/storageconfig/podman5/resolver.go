// Package podman5 implements the Podman 5 rootless storage configuration policy.
package podman5

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"go.podman.io/storage"
	storageConfig "go.podman.io/storage/pkg/config"
	"go.podman.io/storage/pkg/configfile"
	"go.podman.io/storage/pkg/homedir"
	storageTypes "go.podman.io/storage/types"
	"golang.org/x/sys/unix"
)

// Resolve applies Podman 5's storage v1.62.2 rootless inheritance policy. The newer
// reader library applies rootful options wholesale; using it as the rootless
// resolver would silently attach system image stores and mount options.
// File discovery and TOML types still come from the official storage library.
func Resolve(uid int, load storageTypes.LoadOptions) (storage.StoreOptions, error) {
	if uid == 0 {
		return storageTypes.LoadStoreOptions(load)
	}
	system, user, explicit, err := readStorageConfig(uid, load)
	if err != nil {
		return storage.StoreOptions{}, err
	}
	data, err := homedir.GetDataHome()
	if err != nil {
		return storage.StoreOptions{}, err
	}
	runtime, err := homedir.GetRuntimeDir()
	if err != nil {
		return storage.StoreOptions{}, err
	}
	graph, run := filepath.Join(data, "containers", "storage"), filepath.Join(runtime, "containers")
	if path := system.Storage.RootlessStoragePath; path != "" {
		graph = expandStoragePath(path, uid)
	}
	if explicit {
		// User/explicit configuration is authoritative. Missing roots get
		// per-user defaults; other system settings are not merged into it.
		opts := configuredOptions(user)
		if opts.GraphRoot == "" {
			opts.GraphRoot = graph
			if user.Storage.RootlessStoragePath != "" {
				opts.GraphRoot = user.Storage.RootlessStoragePath
			}
		}
		if opts.RunRoot == "" {
			opts.RunRoot = run
		}
		opts.GraphRoot = expandStoragePath(opts.GraphRoot, uid)
		opts.RunRoot = expandStoragePath(opts.RunRoot, uid)
		return opts, nil
	}
	base := configuredOptions(system)
	opts := storage.StoreOptions{GraphRoot: graph, RunRoot: run, PullOptions: base.PullOptions}
	if slices.Contains([]string{"overlay", "vfs", "btrfs"}, base.GraphDriverName) {
		opts.GraphDriverName = base.GraphDriverName
	}
	if driver := os.Getenv("STORAGE_DRIVER"); driver != "" {
		opts.GraphDriverName = normalizeDriver(driver)
	}
	// Podman carries only the overlay chown compatibility option from the
	// system driver configuration, not additional stores or mount options.
	if opts.GraphDriverName == "overlay" {
		for _, option := range base.GraphDriverOptions {
			if strings.Contains(option, "ignore_chown_errors") {
				opts.GraphDriverOptions = append(opts.GraphDriverOptions, option)
				break
			}
		}
	}
	if opts.GraphDriverName == "" {
		opts.GraphDriverPriority = base.GraphDriverPriority
		if len(opts.GraphDriverPriority) == 0 {
			opts.GraphDriverName = rootlessDriver(graph)
		}
	}
	if options := os.Getenv("STORAGE_OPTS"); options != "" {
		opts.GraphDriverOptions = append(opts.GraphDriverOptions, strings.Split(options, ",")...)
	}
	return opts, nil
}

func readStorageConfig(uid int, load storageTypes.LoadOptions) (system, user storageTypes.TomlConfig, explicit bool, err error) {
	userDir, err := configfile.UserConfigPath()
	if err != nil {
		return system, user, false, err
	}
	file := &configfile.File{Name: "storage", Extension: "conf", EnvironmentName: "CONTAINERS_STORAGE_CONF", UserId: uid, RootForImplicitAbsolutePaths: load.RootForImplicitAbsolutePaths}
	for item, readErr := range configfile.Read(file) {
		if readErr != nil {
			return system, user, false, readErr
		}
		target := &system
		if rootlessConfigSource(item.Name, userDir) {
			target, explicit = &user, true
		}
		if _, err := toml.NewDecoder(item.Reader).Decode(target); err != nil {
			return system, user, false, fmt.Errorf("decode storage configuration %s: %w", item.Name, err)
		}
	}
	return system, user, explicit, nil
}

func rootlessConfigSource(path, userDir string) bool {
	for _, name := range []string{"CONTAINERS_STORAGE_CONF", "CONTAINERS_STORAGE_CONF_OVERRIDE"} {
		if value := os.Getenv(name); value != "" && filepath.Clean(value) == filepath.Clean(path) {
			return true
		}
	}
	return strings.HasPrefix(path, userDir+string(filepath.Separator)) || strings.Contains(path, "/storage.rootless.conf.d/")
}

// configuredOptions converts the official TOML model into reader options. Keep
// this conversion separate from inheritance, so explicit configuration retains
// all supported options, including intentionally configured additional stores.
func configuredOptions(config storageTypes.TomlConfig) storage.StoreOptions {
	s := config.Storage
	o := s.Options
	opts := storage.StoreOptions{
		GraphRoot: s.GraphRoot, RunRoot: s.RunRoot, ImageStore: s.ImageStore,
		GraphDriverName: s.Driver, GraphDriverPriority: s.DriverPriority.Values,
		PullOptions: o.PullOptions, DisableVolatile: o.DisableVolatile, TransientStore: s.TransientStore,
		RootAutoNsUser: o.RootAutoUsernsUser, AutoNsMinSize: o.AutoUsernsMinSize, AutoNsMaxSize: o.AutoUsernsMaxSize,
	}
	if driver := os.Getenv("STORAGE_DRIVER"); driver != "" {
		opts.GraphDriverName = driver
	}
	opts.GraphDriverName = normalizeDriver(opts.GraphDriverName)
	for _, path := range o.AdditionalImageStores.Values {
		opts.GraphDriverOptions = append(opts.GraphDriverOptions, "imagestore="+path)
	}
	for _, path := range o.AdditionalLayerStores.Values {
		opts.GraphDriverOptions = append(opts.GraphDriverOptions, "additionallayerstore="+path)
	}
	for _, option := range []struct{ key, value string }{
		{"size", o.Size}, {"mount_program", o.MountProgram}, {"skip_mount_home", o.SkipMountHome},
		{"ignore_chown_errors", o.IgnoreChownErrors}, {"mountopt", o.MountOpt},
	} {
		if option.value != "" {
			opts.GraphDriverOptions = append(opts.GraphDriverOptions, option.key+"="+option.value)
		}
	}
	if o.ForceMask != 0 {
		opts.GraphDriverOptions = append(opts.GraphDriverOptions, "force_mask="+strconv.FormatUint(uint64(o.ForceMask), 8))
	}
	for _, option := range storageConfig.GetGraphDriverOptions(o) {
		if opts.GraphDriverName != "" && strings.HasPrefix(option, opts.GraphDriverName+".") {
			opts.GraphDriverOptions = append(opts.GraphDriverOptions, option)
		}
	}
	if options, ok := os.LookupEnv("STORAGE_OPTS"); ok {
		opts.GraphDriverOptions = nil
		if options != "" {
			opts.GraphDriverOptions = strings.Split(options, ",")
		}
	}
	return opts
}

func normalizeDriver(driver string) string {
	if driver == "overlay2" {
		return "overlay"
	}
	return driver
}

func rootlessDriver(graph string) string {
	entries, _ := os.ReadDir(graph)
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), "-images"); ok && entry.IsDir() {
			return name
		}
	}
	// Match Podman's pre-namespace heuristic; mounting an overlay here would
	// require capabilities the caller has not acquired yet.
	if path, _ := exec.LookPath("fuse-overlayfs"); path != "" {
		return "overlay"
	}
	var uts unix.Utsname
	if unix.Uname(&uts) == nil {
		parts := strings.Split(string(uts.Release[:]), ".")
		major, _ := strconv.Atoi(parts[0])
		if major >= 6 {
			return "overlay"
		}
		if major == 5 && len(parts) > 1 {
			minor, _ := strconv.Atoi(parts[1])
			if minor >= 13 {
				return "overlay"
			}
		}
	}
	return "vfs"
}

func expandStoragePath(path string, uid int) string {
	path = os.ExpandEnv(strings.ReplaceAll(path, "$UID", strconv.Itoa(uid)))
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
