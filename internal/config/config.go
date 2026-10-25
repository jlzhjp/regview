// Package config resolves Viper sources once into validated, immutable settings.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

type Config struct {
	Backends                  []string      `mapstructure:"backends" json:"backends"`
	Hostnames                 []string      `mapstructure:"hostnames" json:"hostnames"`
	RegistryListen            string        `mapstructure:"registry-listen" json:"registry-listen"`
	AdminListen               string        `mapstructure:"admin-listen" json:"admin-listen"`
	DockerEngineSocket        string        `mapstructure:"docker-engine-socket" json:"docker-engine-socket"`
	DockerSocket              string        `mapstructure:"docker-socket" json:"docker-socket"`
	DockerNamespace           string        `mapstructure:"docker-namespace" json:"docker-namespace"`
	PodmanGraphRoot           string        `mapstructure:"podman-graphroot" json:"podman-graphroot"`
	PodmanRunRoot             string        `mapstructure:"podman-runroot" json:"podman-runroot"`
	PodmanDriver              string        `mapstructure:"podman-driver" json:"podman-driver"`
	PodmanBlobCacheTTL        time.Duration `mapstructure:"podman-blob-cache-ttl" json:"podman-blob-cache-ttl"`
	PodmanBlobCacheMaxBytes   int64         `mapstructure:"podman-blob-cache-max-bytes" json:"podman-blob-cache-max-bytes"`
	PodmanBlobCacheMaxEntries int           `mapstructure:"podman-blob-cache-max-entries" json:"podman-blob-cache-max-entries"`
}

func Defaults(uid int) Config {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", uid)
	}
	engineSocket := filepath.Join(runtimeDir, "docker.sock")
	if uid == 0 {
		engineSocket = "/var/run/docker.sock"
	}
	return Config{RegistryListen: "127.0.0.1:5000", AdminListen: "127.0.0.1:8080", DockerEngineSocket: engineSocket, PodmanBlobCacheTTL: 2 * time.Minute, PodmanBlobCacheMaxBytes: 1 << 30, PodmanBlobCacheMaxEntries: 64}
}

// AutoBackends reports whether backend selection was omitted from all sources.
func (c Config) AutoBackends() bool { return c.Backends == nil }

func Flags(flags *pflag.FlagSet, d Config) {
	flags.StringSlice("backends", d.Backends, "Image backends: docker, podman, or both (default: discover usable local backends)")
	flags.StringSlice("hostnames", nil, "Exact registry authorities to expose, including ports (required)")
	flags.String("registry-listen", d.RegistryListen, "Registry HTTP listen address")
	flags.String("admin-listen", d.AdminListen, "Admin HTTP listen address")
	flags.String("docker-engine-socket", d.DockerEngineSocket, "Local Docker engine socket used for discovery")
	flags.String("docker-socket", d.DockerSocket, "Explicit containerd socket; bypass Docker discovery")
	flags.String("docker-namespace", d.DockerNamespace, "Containerd image namespace (default: discover, otherwise moby)")
	flags.String("podman-graphroot", d.PodmanGraphRoot, "Override Podman storage graph root")
	flags.String("podman-runroot", d.PodmanRunRoot, "Override Podman storage runtime root")
	flags.String("podman-driver", d.PodmanDriver, "Override Podman storage driver")
	flags.Duration("podman-blob-cache-ttl", d.PodmanBlobCacheTTL, "Verified Podman reader lifetime; 0 disables caching")
	flags.Int64("podman-blob-cache-max-bytes", d.PodmanBlobCacheMaxBytes, "Maximum retained Podman blob cache bytes")
	flags.Int("podman-blob-cache-max-entries", d.PodmanBlobCacheMaxEntries, "Maximum retained Podman blob readers")
}

func Load(flags *pflag.FlagSet, file string, uid int) (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("REGVIEW")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()
	var bindError error
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Name == "config" || flag.Name == "dry-run" || flag.Name == "help" || flag.Name == "version" {
			return
		}
		bindError = errors.Join(bindError, v.BindPFlag(flag.Name, flag), v.BindEnv(flag.Name))
	})
	if bindError != nil {
		return Config{}, bindError
	}
	if file != "" {
		v.SetConfigFile(file)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, err
		}
	} else {
		v.SetConfigName("regview")
		v.SetConfigType("yaml")
		if uid == 0 {
			v.AddConfigPath("/etc/regview")
		} else if dir, err := os.UserConfigDir(); err == nil {
			v.AddConfigPath(filepath.Join(dir, "regview"))
		}
		if err := v.ReadInConfig(); err != nil {
			var missing viper.ConfigFileNotFoundError
			if !errors.As(err, &missing) {
				return Config{}, err
			}
		}
	}
	var config Config
	if err := v.UnmarshalExact(&config); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	// A bound flag's default is not an explicit selection. Keep omission distinct
	// from an empty list in a file, environment variable, or changed flag.
	_, envBackends := os.LookupEnv("REGVIEW_BACKENDS")
	if !flags.Changed("backends") && !v.InConfig("backends") && !envBackends {
		config.Backends = nil
	} else if len(config.Backends) == 0 {
		return Config{}, fmt.Errorf("explicit backends must not be empty")
	}
	return config, config.Validate()
}

func (c Config) Validate() error {
	if c.PodmanBlobCacheTTL < 0 || c.PodmanBlobCacheMaxBytes < 0 || c.PodmanBlobCacheMaxEntries < 0 {
		return fmt.Errorf("Podman blob cache settings must not be negative")
	}
	if c.PodmanBlobCacheTTL > 0 && (c.PodmanBlobCacheMaxBytes == 0 || c.PodmanBlobCacheMaxEntries == 0) {
		return fmt.Errorf("enabled Podman blob cache requires positive byte and entry limits")
	}
	if c.Backends != nil && len(c.Backends) == 0 {
		return fmt.Errorf("explicit backends must not be empty")
	}
	seen := make(map[string]bool)
	for _, backend := range c.Backends {
		if !slices.Contains([]string{"docker", "podman"}, backend) {
			return fmt.Errorf("unknown backend %q", backend)
		}
		if seen[backend] {
			return fmt.Errorf("duplicate backend %q", backend)
		}
		seen[backend] = true
	}
	if len(c.Hostnames) == 0 {
		return fmt.Errorf("specify at least one registry authority using --hostnames or hostnames in configuration")
	}
	for _, host := range c.Hostnames {
		u, err := url.Parse("http://" + host)
		if err != nil || u.Host != host || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.ToLower(host) != host {
			return fmt.Errorf("invalid registry authority %q", host)
		}
		named, err := reference.WithName(host + "/probe")
		if err != nil || reference.Domain(named) != host {
			return fmt.Errorf("invalid registry authority %q", host)
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("invalid registry port in %q", host)
			}
		}
	}
	for _, address := range []string{c.RegistryListen, c.AdminListen} {
		if _, err := net.ResolveTCPAddr("tcp", address); err != nil {
			return fmt.Errorf("invalid listen address %q: %w", address, err)
		}
	}
	if c.RegistryListen == c.AdminListen {
		return fmt.Errorf("registry and admin listen addresses must differ")
	}
	for _, path := range []string{c.DockerEngineSocket, c.DockerSocket, c.PodmanGraphRoot, c.PodmanRunRoot} {
		if path != "" && !filepath.IsAbs(path) {
			return fmt.Errorf("storage/socket path must be absolute: %q", path)
		}
	}
	return nil
}
