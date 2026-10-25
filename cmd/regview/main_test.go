package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDryRunDoesNotOpenBackendOrListener(t *testing.T) {
	cmd := command()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--dry-run", "--backends", "podman", "--hostnames", "example.test:5000", "--podman-graphroot", "/does/not/exist"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "example.test:5000") {
		t.Fatal("missing resolved config")
	}
}

func TestNonPodmanModesDoNotProbeVersion(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		name := "docker only"
		if dryRun {
			name = "dry run"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "podman-ran")
			t.Setenv("REGVIEW_TEST_PODMAN_MARKER", marker)
			if err := os.WriteFile(filepath.Join(dir, "podman"), []byte("#!/bin/sh\nprintf ran > \"$REGVIEW_TEST_PODMAN_MARKER\"\nprintf '%s' '{\"Client\":{\"Version\":\"5.8.9\"}}'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			cmd := command()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			args := []string{"--hostnames", "example.test"}
			if dryRun {
				args = append(args, "--dry-run")
			} else {
				args = append(args, "--backends", "docker", "--docker-engine-socket", filepath.Join(dir, "missing.sock"))
			}
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(t.Context())
			if dryRun && err != nil {
				t.Fatal(err)
			}
			if !dryRun && err == nil {
				t.Fatal("missing Docker socket was accepted")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("Podman was invoked: %v", err)
			}
		})
	}
}

func TestAutomaticDryRunDoesNotResolveStorage(t *testing.T) {
	t.Setenv("CONTAINERS_STORAGE_CONF", "/does/not/exist/storage.conf")
	cmd := command()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--dry-run", "--hostnames", "example.test", "--docker-engine-socket", "/does/not/exist/docker.sock", "--registry-listen", "127.0.0.1:1"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"backends": null`) {
		t.Fatal("automatic selection not preserved:", output.String())
	}
}
