package main

import (
	"bytes"
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
