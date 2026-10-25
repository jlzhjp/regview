package podman

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"
	"regview/internal/catalog"
	"regview/internal/image"
	"regview/internal/registry"
)

type fixtureBackend struct {
	records []image.Record
	sources map[string]*source
	cache   *blobCache
}

func (*fixtureBackend) Name() string                                   { return "podman" }
func (b *fixtureBackend) Close() error                                 { return b.cache.Close() }
func (b *fixtureBackend) List(context.Context) ([]image.Record, error) { return b.records, nil }
func (b *fixtureBackend) prepare(ctx context.Context, record image.Record, wanted digest.Digest) (*image.Artifact, error) {
	s := newRequest(func(_ context.Context, id string) (types.ImageSource, error) {
		src, exists := b.sources[id]
		if !exists {
			return nil, image.ErrNotFound
		}
		return src, nil
	}, nil)
	s.cache = b.cache
	s.wantedBlob = wanted
	a := image.NewArtifact(record.Target)
	a.Content = s
	root, err := s.prepare(ctx, record.ID, record.Target, a, 0)
	if err != nil {
		_ = a.Close()
		return nil, err
	}
	a.Root = root
	return a, nil
}
func (b *fixtureBackend) Prepare(ctx context.Context, r image.Record) (*image.Artifact, error) {
	return b.prepare(ctx, r, "")
}
func (b *fixtureBackend) ResolveBlob(ctx context.Context, r image.Record, d digest.Digest) (*image.BlobRef, error) {
	a, err := b.prepare(ctx, r, d)
	if err != nil {
		return nil, err
	}
	return image.FindBlob(a, d)
}

func TestPodmanHTTPMetadataRangesAndIsolation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	src, record := fixture(t, 0, 0)
	record.Reference = "example.test/team/api:v1"
	private, privateRecord := fixture(t, 0, 0, 0)
	privateRecord.ID = "private-id"
	privateRecord.Reference = "private.test/secret:v1"
	backend := &fixtureBackend{records: []image.Record{record, privateRecord}, sources: map[string]*source{record.ID: src, privateRecord.ID: private}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := catalog.New([]image.Backend{backend}, []string{"example.test", "private.test"}, logger)
	handler := registry.New(c, logger)
	request := func(method, host, path string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), method, "http://"+host+path, nil)
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	root := request("GET", "example.test", "/v2/team/api/manifests/v1", nil)
	if root.Code != 200 {
		t.Fatalf("manifest: %d %s", root.Code, root.Body)
	}
	generated := root.Header().Get("Docker-Content-Digest")
	if generated == record.Target.Digest.String() {
		t.Fatal("manifest was not converted")
	}
	for _, info := range src.layers {
		if src.readCount(info.Digest) != 0 {
			t.Fatal("manifest exported known-size layer")
		}
	}
	byDigest := request("GET", "example.test", "/v2/team/api/manifests/"+generated, nil)
	if byDigest.Code != 200 || !bytes.Equal(byDigest.Body.Bytes(), root.Body.Bytes()) {
		t.Fatalf("generated digest resolution: %d %s", byDigest.Code, byDigest.Body)
	}
	if w := request("GET", "example.test", "/v2/team/api/manifests/"+record.Target.Digest.String(), nil); w.Code != 404 {
		t.Fatal("original digest aliased modified manifest")
	}
	d := src.layers[0].Digest
	path := "/v2/team/api/blobs/" + d.String()
	for _, tc := range []struct {
		method  string
		headers map[string]string
		status  int
	}{
		{"HEAD", nil, 200},
		{"GET", map[string]string{"If-None-Match": strconv.Quote(d.String())}, 304},
		{"GET", map[string]string{"If-Match": "\"different\""}, 412},
	} {
		w := request(tc.method, "example.test", path, tc.headers)
		if w.Code != tc.status || w.Body.Len() != 0 {
			t.Fatalf("metadata-only: %d %s", w.Code, w.Body)
		}
		if src.readCount(d) != 0 {
			t.Fatal("HEAD/conditional request exported known-size layer")
		}
	}
	for _, headers := range []map[string]string{
		{"Range": "bytes=2-5"},
		{"Range": "bytes=2-5", "If-Range": strconv.Quote(d.String())},
	} {
		w := request("GET", "example.test", path, headers)
		if w.Code != 206 || w.Body.String() != string(src.blobs[d][2:6]) || w.Header().Get("Content-Range") != "bytes 2-5/100" {
			t.Fatalf("range: %d %s %v", w.Code, w.Body, w.Header())
		}
	}
	if src.readCount(d) != 2 {
		t.Fatal("repeated range request reused export")
	}
	if src.readCount(src.layers[1].Digest) != 0 {
		t.Fatal("requested layer caused unrelated export")
	}
	privateManifest := request("GET", "private.test", "/v2/secret/manifests/v1", nil)
	for _, target := range []string{
		"/v2/team/api/manifests/" + privateManifest.Header().Get("Docker-Content-Digest"),
		"/v2/team/api/blobs/" + private.layers[2].Digest.String(),
		"/v2/another/blobs/" + d.String(),
	} {
		if w := request("GET", "example.test", target, nil); w.Code != 404 {
			t.Fatalf("digest escaped repository: %s %d", target, w.Code)
		}
	}
	// Corrupt bytes must fail before any layer data or success status is sent.
	src.blobs[d] = []byte("corrupted export")
	if w := request("GET", "example.test", path, nil); w.Code != 503 || strings.Contains(w.Body.String(), "corrupted export") {
		t.Fatalf("unverified response: %d %s", w.Code, w.Body)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files leaked: %v %v", files, err)
	}
}

func TestBlobResolutionDoesNotMeasureOtherUnknownLayers(t *testing.T) {
	src, record := fixture(t, 0, -1)
	b := &fixtureBackend{sources: map[string]*source{record.ID: src}}
	known, unknown := src.layers[0].Digest, src.layers[1].Digest
	ref, err := b.ResolveBlob(t.Context(), record, known)
	if err != nil {
		t.Fatal(err)
	}
	if err := ref.Close(); err != nil {
		t.Fatal(err)
	}
	if src.readCount(known) != 0 || src.readCount(unknown) != 0 {
		t.Fatal("blob metadata measured an unrelated layer")
	}
	ref, err = b.ResolveBlob(t.Context(), record, unknown)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := ref.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = blob.Close()
	_ = ref.Close()
	if src.readCount(unknown) != 1 {
		t.Fatal("requested unknown layer not measured once")
	}
	_, err = b.ResolveBlob(t.Context(), record, digest.FromString("unrelated blob"))
	if !errors.Is(err, image.ErrNotFound) || src.readCount(unknown) != 1 {
		t.Fatal("unknown digest caused unrelated reconstruction")
	}
}
