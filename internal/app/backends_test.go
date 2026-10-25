package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"regview/internal/image"
	"regview/internal/testutil"
)

type trackedBackend struct {
	*testutil.Backend
	closed int
}

func (b *trackedBackend) Close() error { b.closed++; return nil }

func TestBackendSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection []string
		unusable  string
		listError bool
		want      []string
		wantError bool
	}{
		{name: "discover both", want: []string{"docker", "podman"}},
		{name: "discover docker", unusable: "podman", want: []string{"docker"}},
		{name: "discover podman", unusable: "docker", want: []string{"podman"}},
		{name: "skip unreadable metadata", unusable: "podman", listError: true, want: []string{"docker"}},
		{name: "explicit failure", selection: []string{"docker", "podman"}, unusable: "podman", wantError: true},
		{name: "explicit list failure", selection: []string{"docker", "podman"}, unusable: "podman", listError: true, wantError: true},
		{name: "explicit podman only", selection: []string{"podman"}, want: []string{"podman"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			opened := make(map[string]*trackedBackend)
			var attempted []string
			open := func(_ context.Context, name string) (image.Backend, error) {
				attempted = append(attempted, name)
				if name == tc.unusable && !tc.listError {
					return nil, errors.New("fixture unavailable")
				}
				b := &trackedBackend{Backend: testutil.NewBackend(name)}
				if name == tc.unusable {
					b.ListError = errors.New("fixture metadata unreadable")
				}
				opened[name] = b
				return b, nil
			}
			backends, err := openBackends(t.Context(), tc.selection, open, logger)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			var names []string
			for _, b := range backends {
				names = append(names, b.Name())
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("enabled %v, want %v", names, tc.want)
			}
			if tc.selection != nil && !reflect.DeepEqual(attempted, tc.selection) {
				t.Fatalf("explicit selection probed other backends: %v", attempted)
			}
			if tc.selection == nil && tc.unusable != "" && !strings.Contains(logs.String(), "skip unavailable local backend") {
				t.Fatal("missing discovery diagnostic")
			}
			closeBackends(backends, logger)
			for name, b := range opened {
				if b.closed != 1 || b.Lists.Load() != 1 {
					t.Errorf("%s: closes=%d lists=%d", name, b.closed, b.Lists.Load())
				}
			}
		})
	}
}

func TestNoBackendAndCanceledDiscovery(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	_, err := openBackends(t.Context(), nil, func(_ context.Context, name string) (image.Backend, error) {
		return nil, errors.New(name + " unavailable")
	}, logger)
	if err == nil || !strings.Contains(err.Error(), "docker unavailable") || !strings.Contains(err.Error(), "podman unavailable") {
		t.Fatalf("missing aggregated diagnostics: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	b := &trackedBackend{Backend: testutil.NewBackend("docker")}
	_, err = openBackends(ctx, nil, func(context.Context, string) (image.Backend, error) {
		cancel()
		return b, nil
	}, logger)
	if !errors.Is(err, context.Canceled) || b.closed != 1 {
		t.Fatalf("canceled discovery: err=%v closes=%d", err, b.closed)
	}
}
