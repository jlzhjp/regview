package storageconfig

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseClientVersion(t *testing.T) {
	for _, tc := range []struct {
		input, text string
		major       int
	}{
		{`{"Client":{"Version":"5.8.9","GoVersion":"go1.26"}}`, "5.8.9", 5},
		{`{"Client":{"Version":"6.0.0-dev+distribution.1"}}`, "6.0.0-dev+distribution.1", 6},
		{`{"Client":{"Version":"6.1.0"},"Server":{"Version":"5.8.9"}}`, "6.1.0", 6},
	} {
		v, err := parseVersion(tc.input)
		if err != nil || v.major != tc.major || v.text != tc.text {
			t.Fatalf("parse %s: %+v, %v", tc.input, v, err)
		}
	}
	for _, input := range []string{
		`{`, `null`, `{}`, `{"Server":{"Version":"5.8.9"}}`,
		`{"Client":{"Version":5}}`, `{"Client":{"Version":"5"}}`,
		`{"Client":{"Version":"latest"}}`, `{"Client":{"Version":"5.8.9"}} garbage`,
		`{"Client":{"Version":"999999999999999999999999.0.0"}}`,
	} {
		if _, err := parseVersion(input); err == nil {
			t.Fatalf("accepted invalid version report %s", input)
		}
	}
}

func fakePodman(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestLocalVersionUsesStructuredLocalCommand(t *testing.T) {
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("REGVIEW_TEST_ARGS", args)
	fakePodman(t, `printf '%s\n' "$@" > "$REGVIEW_TEST_ARGS"
printf '%s\n' '{"Client":{"Version":"5.8.9"}}'
`)
	v, err := localVersion(t.Context())
	if err != nil || v.major != 5 || v.text != "5.8.9" {
		t.Fatalf("version: %+v, %v", v, err)
	}
	data, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--remote=false", "version", "--format", "json"}
	if got := strings.Fields(string(data)); !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments %v, want %v", got, want)
	}
}

func TestLocalVersionCommandFailures(t *testing.T) {
	t.Run("missing executable", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := localVersion(t.Context()); !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("missing executable: %v", err)
		}
	})
	t.Run("stderr diagnostic", func(t *testing.T) {
		fakePodman(t, "echo 'cannot initialize local configuration' >&2\nexit 7\n")
		if _, err := localVersion(t.Context()); err == nil || !strings.Contains(err.Error(), "cannot initialize local configuration") {
			t.Fatalf("missing CLI diagnostic: %v", err)
		}
	})
	t.Run("invalid JSON", func(t *testing.T) {
		fakePodman(t, "printf 'not JSON'\n")
		if _, err := localVersion(t.Context()); err == nil {
			t.Fatal("accepted invalid output")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		fakePodman(t, "exit 0\n")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := localVersion(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled probe: %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		fakePodman(t, "exec /bin/sleep 30\n")
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		defer cancel()
		if _, err := localVersion(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("probe deadline: %v", err)
		}
	})
}
