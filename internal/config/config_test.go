package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestPrecedenceAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("backends: [podman]\nhostnames: [example.test]\nadmin-listen: 127.0.0.1:9000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REGVIEW_ADMIN_LISTEN", "127.0.0.1:9001")
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	Flags(flags, Defaults(1000))
	if err := flags.Parse([]string{"--admin-listen", "127.0.0.1:9002"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(flags, path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminListen != "127.0.0.1:9002" || cfg.Backends[0] != "podman" {
		t.Fatalf("precedence: %+v", cfg)
	}
	if err := os.WriteFile(path, []byte("hostnames: [example.test]\nunknown-option: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(flags, path, 1000); err == nil {
		t.Fatal("unknown option was ignored")
	}
}

func TestBackendSelectionSources(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, env string
		args            []string
		setEnv, invalid bool
		want            string
	}{
		{name: "omitted"},
		{name: "file", yaml: "backends: [podman]\n", want: "podman"},
		{name: "environment", env: "docker,podman", setEnv: true, want: "docker,podman"},
		{name: "flag wins", yaml: "backends: [podman]\n", env: "podman", setEnv: true, args: []string{"--backends", "docker"}, want: "docker"},
		{name: "empty file", yaml: "backends: []\n", invalid: true},
		{name: "null file is unset", yaml: "backends: null\n"},
		{name: "empty environment", setEnv: true, invalid: true},
		{name: "empty flag", args: []string{"--backends="}, invalid: true},
		{name: "unknown", args: []string{"--backends", "auto"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv("REGVIEW_BACKENDS", tc.env)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("hostnames: [example.test]\n"+tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			Flags(flags, Defaults(1000))
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(flags, path, 1000)
			if (err != nil) != tc.invalid {
				t.Fatalf("error=%v", err)
			}
			if err == nil && (strings.Join(cfg.Backends, ",") != tc.want || cfg.AutoBackends() != (tc.want == "")) {
				t.Fatalf("selection: %+v", cfg)
			}
		})
	}
}

func TestUserDefaultsAndExactAuthorities(t *testing.T) {
	if got := Defaults(0).DockerEngineSocket; got != "/var/run/docker.sock" {
		t.Fatal(got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1234")
	if got := Defaults(1234).DockerEngineSocket; got != "/run/user/1234/docker.sock" {
		t.Fatal(got)
	}
	for _, host := range []string{"example.test", "example.test:5000", "localhost:5000", "[::1]:5000"} {
		cfg := Defaults(1000)
		cfg.Hostnames = []string{host}
		if err := cfg.Validate(); err != nil {
			t.Errorf("valid host %s: %v", host, err)
		}
	}
	for _, host := range []string{"https://example.test", "example.test/path", "example.test:99999", "EXAMPLE.test", "example.test?x=1", "example.test#x"} {
		cfg := Defaults(1000)
		cfg.Hostnames = []string{host}
		if err := cfg.Validate(); err == nil {
			t.Errorf("accepted invalid host %s", host)
		}
	}
}

func TestRemovedConfigurationIsRejected(t *testing.T) {
	for _, key := range []string{"cache-dir", "refresh-interval"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("hostnames: [example.test]\n"+key+": obsolete\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
		Flags(flags, Defaults(1000))
		if flags.Lookup(key) != nil {
			t.Fatalf("obsolete flag remains: %s", key)
		}
		if _, err := Load(flags, path, 1000); err == nil {
			t.Fatalf("obsolete field accepted: %s", key)
		}
	}
}

func TestPodmanCacheConfiguration(t *testing.T) {
	t.Setenv("REGVIEW_PODMAN_BLOB_CACHE_TTL", "15s")
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	Flags(flags, Defaults(1000))
	if err := flags.Parse([]string{"--hostnames", "example.test", "--podman-blob-cache-max-bytes", "4096", "--podman-blob-cache-max-entries", "2"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(flags, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PodmanBlobCacheTTL.String() != "15s" || cfg.PodmanBlobCacheMaxBytes != 4096 || cfg.PodmanBlobCacheMaxEntries != 2 {
		t.Fatalf("cache settings: %+v", cfg)
	}
	cfg.PodmanBlobCacheMaxEntries = 0
	if cfg.Validate() == nil {
		t.Fatal("enabled cache accepted zero capacity")
	}
	cfg.PodmanBlobCacheTTL = 0
	if err := cfg.Validate(); err != nil {
		t.Fatal("disabled cache rejected:", err)
	}
	cfg.PodmanBlobCacheMaxBytes = -1
	if cfg.Validate() == nil {
		t.Fatal("negative limit accepted")
	}
}
