package catalog

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"regview/internal/image"
	"regview/internal/testutil"
)

func TestNamesPreserveExplicitAuthorities(t *testing.T) {
	for _, tc := range []struct{ input, host, repo, tag string }{
		{"alpine", "docker.io", "library/alpine", "latest"},
		{"team/api:v1", "docker.io", "team/api", "v1"},
		{"index.docker.io/team/api:v1", "index.docker.io", "team/api", "v1"},
		{"localhost:5000/team/api:v1", "localhost:5000", "team/api", "v1"},
	} {
		name, err := ParseName(tc.input)
		if err != nil {
			t.Fatal(err)
		}
		if name.Host != tc.host || name.Repository != tc.repo || name.Tag != tc.tag {
			t.Errorf("%s: %+v", tc.input, name)
		}
	}
}

func TestLiveListingsAndLookups(t *testing.T) {
	b := testutil.NewBackend("docker")
	c := New([]image.Backend{b}, []string{"example.test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if repos, err := c.Repositories(t.Context(), "example.test"); err != nil || len(repos) != 0 {
		t.Fatalf("empty listing: %v %v", repos, err)
	}
	first, _ := b.Add("example.test/app:v1", "one")
	if got, err := c.Manifest(t.Context(), "example.test", "app", "v1"); err != nil || got.Descriptor.Digest != first.Digest {
		t.Fatalf("new image: %v", err)
	}
	if rows := c.View(t.Context()).Rows; len(rows) != 1 {
		t.Fatalf("viewer missed new image: %+v", rows)
	}
	b.Remove("example.test/app:v1", first.Digest)
	if _, err := c.Manifest(t.Context(), "example.test", "app", "v1"); !errors.Is(err, image.ErrNotFound) {
		t.Fatalf("deleted image still resolves: %v", err)
	}
	if rows := c.View(t.Context()).Rows; len(rows) != 0 {
		t.Fatal("viewer retained deleted image")
	}
	b.ListError = errors.New("engine down")
	if _, err := c.Repositories(t.Context(), "example.test"); err == nil {
		t.Fatal("backend failure hidden")
	}
	view := c.View(t.Context())
	if len(view.Rows) != 0 || view.Status[0].Error == "" {
		t.Fatal("viewer retained inventory during failure")
	}
}

func TestConcurrentRetagAndDeletionPinSelectedRecord(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "retag", true: "delete"}[deleted], func(t *testing.T) {
			docker, podman := testutil.NewBackend("docker"), testutil.NewBackend("podman")
			old, _ := docker.Add("example.test/app:v1", "old Docker")
			newTarget, _ := docker.Add("example.test/app:replacement", "new Docker")
			podman.Add("example.test/app:v1", "Podman must not replace selected Docker image")
			listed, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			docker.BeforePrepare = func(record image.Record) {
				if record.Reference == "example.test/app:v1" {
					once.Do(func() { close(listed); <-resume })
				}
			}
			c := New([]image.Backend{podman, docker}, []string{"example.test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			type result struct {
				manifest image.Manifest
				err      error
			}
			finished := make(chan result, 1)
			go func() { m, err := c.Manifest(t.Context(), "example.test", "app", "v1"); finished <- result{m, err} }()
			<-listed
			if deleted {
				docker.Remove("example.test/app:v1", old.Digest)
			}
			docker.Retag("example.test/app:v1", image.Record{ID: newTarget.Digest.String(), Target: newTarget})
			close(resume)
			got := <-finished
			if deleted {
				if !errors.Is(got.err, image.ErrNotFound) {
					t.Fatalf("deleted pinned record: %v", got.err)
				}
			} else if got.err != nil || got.manifest.Descriptor.Digest != old.Digest {
				t.Fatalf("in-flight image changed: %+v", got)
			}
			current, err := c.Manifest(t.Context(), "example.test", "app", "v1")
			if err != nil || current.Descriptor.Digest != newTarget.Digest {
				t.Fatalf("next request retained old record: %v", err)
			}
		})
	}
}

func TestConcurrentRequestsSeeLiveRecords(t *testing.T) {
	b := testutil.NewBackend("docker")
	x, _ := b.Add("example.test/app:x", "x")
	y, _ := b.Add("example.test/app:y", "y")
	b.Retag("example.test/app:latest", image.Record{ID: x.Digest.String(), Target: x})
	c := New([]image.Backend{b}, []string{"example.test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			for _, desc := range []image.Record{{ID: x.Digest.String(), Target: x}, {ID: y.Digest.String(), Target: y}} {
				b.Retag("example.test/app:latest", desc)
			}
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 25 {
				manifest, err := c.Manifest(t.Context(), "example.test", "app", "latest")
				if err != nil || manifest.Descriptor.Digest != x.Digest && manifest.Descriptor.Digest != y.Digest {
					t.Errorf("unpinned result: %v", err)
				}
				_ = c.View(t.Context())
			}
		})
	}
	wg.Wait()
	if b.Lists.Load() < 200 {
		t.Fatal("requests reused inventory")
	}
}

func TestAuthorityAndRepositoryRemainIsolated(t *testing.T) {
	b := testutil.NewBackend("docker")
	manifest, layer := b.Add("other.test/private:v1", "private bytes")
	b.Add("example.test/public:v1", "public bytes")
	c := New([]image.Backend{b}, []string{"example.test", "other.test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, repo := range []string{"public", "private"} {
		if _, err := c.Manifest(t.Context(), "example.test", repo, manifest.Digest.String()); !errors.Is(err, image.ErrNotFound) {
			t.Fatalf("manifest leaked: %s %v", repo, err)
		}
		if _, err := c.Blob(t.Context(), "example.test", repo, layer.Digest); !errors.Is(err, image.ErrNotFound) {
			t.Fatalf("blob leaked: %s %v", repo, err)
		}
	}
}
