package podman

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/image/v5/types"
	"regview/internal/image"
)

type source struct {
	types.ImageSource
	manifest  []byte
	mediaType string
	blobs     map[digest.Digest][]byte
	layers    []types.BlobInfo
	mu        sync.Mutex
	reads     map[digest.Digest]int
	closes    int
	getBlob   func(context.Context, types.BlobInfo) (io.ReadCloser, int64, error)
}

func (s *source) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	return s.manifest, s.mediaType, nil
}
func (s *source) GetBlob(ctx context.Context, info types.BlobInfo, _ types.BlobInfoCache) (io.ReadCloser, int64, error) {
	s.mu.Lock()
	s.reads[info.Digest]++
	s.mu.Unlock()
	if s.getBlob != nil {
		return s.getBlob(ctx, info)
	}
	data, ok := s.blobs[info.Digest]
	if !ok {
		return nil, 0, image.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}
func (s *source) LayerInfosForCopy(context.Context, *digest.Digest) ([]types.BlobInfo, error) {
	return s.layers, nil
}
func (s *source) Close() error                  { s.mu.Lock(); defer s.mu.Unlock(); s.closes++; return nil }
func (s *source) readCount(d digest.Digest) int { s.mu.Lock(); defer s.mu.Unlock(); return s.reads[d] }

func fixture(t *testing.T, sizes ...int64) (*source, image.Record) {
	t.Helper()
	s := &source{blobs: make(map[digest.Digest][]byte), reads: make(map[digest.Digest]int), mediaType: image.DockerManifest}
	config := oci.Image{Architecture: "amd64", OS: "linux", RootFS: oci.RootFS{Type: "layers"}}
	original := oci.Manifest{SchemaVersion: 2, MediaType: image.DockerManifest}
	for i, size := range sizes {
		data := bytes.Repeat([]byte{byte('a' + i)}, 100+i)
		d := digest.FromBytes(data)
		config.RootFS.DiffIDs = append(config.RootFS.DiffIDs, d)
		s.blobs[d] = data
		if size >= 0 {
			size = int64(len(data))
		}
		s.layers = append(s.layers, types.BlobInfo{Digest: d, Size: size, MediaType: "application/vnd.docker.image.rootfs.diff.tar"})
		original.Layers = append(original.Layers, oci.Descriptor{Digest: digest.FromString("compressed " + d.String()), Size: 99, MediaType: "application/vnd.docker.image.rootfs.diff.tar.gzip"})
	}
	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	original.Config = descriptor(configData, "application/vnd.docker.container.image.v1+json")
	s.blobs[original.Config.Digest] = configData
	s.manifest, err = json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	return s, image.Record{ID: "pinned-storage-id", Target: descriptor(s.manifest, s.mediaType)}
}
func prepareFixture(t *testing.T, ctx context.Context, src *source, record image.Record) *image.Artifact {
	t.Helper()
	request := newRequest(func(_ context.Context, id string) (types.ImageSource, error) {
		if id != record.ID {
			t.Fatalf("source was not pinned: %s", id)
		}
		return src, nil
	}, nil)
	a := image.NewArtifact(record.Target)
	a.Content = request
	root, err := request.prepare(ctx, record.ID, record.Target, a, 0)
	if err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	a.Root = root
	return a
}

func TestManifestUsesMetadataWithoutLayerExports(t *testing.T) {
	src, record := fixture(t, 0, 0)
	a := prepareFixture(t, t.Context(), src, record)
	defer func() { _ = a.Close() }()
	if a.Root.Digest == record.Target.Digest {
		t.Fatal("original digest reused for converted bytes")
	}
	var manifest oci.Manifest
	if err := json.Unmarshal(a.Manifests[a.Root.Digest].Data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.MediaType != oci.MediaTypeImageManifest || manifest.Config.Digest != digest.FromBytes(src.blobs[manifest.Config.Digest]) {
		t.Fatal("configuration changed")
	}
	for i, desc := range manifest.Layers {
		if desc.MediaType != oci.MediaTypeImageLayer || desc.Digest != src.layers[i].Digest || desc.Size != src.layers[i].Size {
			t.Fatalf("descriptor: %+v", desc)
		}
		if src.readCount(desc.Digest) != 0 {
			t.Fatal("exported layer just to generate manifest")
		}
	}
	d := manifest.Layers[0].Digest
	blob, err := a.Content.Open(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blob.Close() }()
	got, err := io.ReadAll(io.NewSectionReader(blob.ReaderAt, 0, blob.Size))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src.blobs[d]) || src.readCount(d) != 1 || src.readCount(manifest.Layers[1].Digest) != 0 {
		t.Fatal("export did not target only requested layer")
	}
}

func TestUnknownSizeMeasuresOnlyAffectedLayerPerRequest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	src, record := fixture(t, 0, -1)
	a := prepareFixture(t, t.Context(), src, record)
	known, unknown := src.layers[0].Digest, src.layers[1].Digest
	if src.readCount(known) != 0 || src.readCount(unknown) != 1 {
		t.Fatal("measured unnecessary layers")
	}
	if a.Blobs[unknown].Size != int64(len(src.blobs[unknown])) {
		t.Fatal("unknown size not measured")
	}
	blob, err := a.Content.Open(t.Context(), unknown)
	if err != nil {
		t.Fatal(err)
	}
	if src.readCount(unknown) != 1 {
		t.Fatal("measured layer exported twice within request")
	}
	if err := blob.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := blob.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("measured reader remains open: %v", err)
	}
	// The next request must repeat measurement, even with identical image state.
	b := prepareFixture(t, t.Context(), src, record)
	_ = b.Close()
	if src.readCount(unknown) != 2 {
		t.Fatal("measurement escaped request lifetime")
	}
	assertNoTemporaryReaders(t, dir)
}

func TestExportVerificationAndCleanup(t *testing.T) {
	for _, mode := range []string{"digest", "size", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			data := []byte("uncompressed tar bytes")
			expected, size := digest.FromBytes(data), int64(len(data))
			if mode == "digest" {
				expected = digest.FromString("different bytes")
			}
			if mode == "size" {
				size++
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			reader := &trackedReader{Reader: bytes.NewReader(data)}
			if _, err := verifyReader(ctx, reader, expected, size); err == nil {
				t.Fatal("accepted invalid/canceled export")
			}
			if reader.closed != 1 {
				t.Fatalf("source not closed: %d", reader.closed)
			}
			assertNoTemporaryReaders(t, dir)
		})
	}
}

func assertNoTemporaryReaders(t *testing.T, dir string) {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary paths leaked: %v %v", files, err)
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		target, _ := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if strings.HasPrefix(target, filepath.Join(dir, "regview-layer-")) {
			t.Fatalf("unlinked temporary reader still open: %s", target)
		}
	}
}

func TestCancellationDuringExportCleansUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	done := make(chan error, 1)
	go func() { _, err := verifyReader(ctx, r, digest.FromString("full layer"), -1); done <- err }()
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("export did not unblock on cancellation")
	}
	assertNoTemporaryReaders(t, dir)
}

func TestUnknownMeasurementClosedAfterLaterFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	src, record := fixture(t, -1, -1)
	src.blobs[src.layers[1].Digest] = []byte("corrupted second layer")
	s := newRequest(func(context.Context, string) (types.ImageSource, error) { return src, nil }, nil)
	a := image.NewArtifact(record.Target)
	a.Content = s
	if _, err := s.prepare(t.Context(), record.ID, record.Target, a, 0); err == nil {
		t.Fatal("accepted corrupt measurement")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if src.closes != 1 {
		t.Fatal("failed preparation did not close source")
	}
	assertNoTemporaryReaders(t, dir)
}

func TestGzipEmptyLayerIsServedUncompressed(t *testing.T) {
	data := make([]byte, 1024)
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	d := digest.FromBytes(compressed.Bytes())
	src := &source{blobs: map[digest.Digest][]byte{d: compressed.Bytes()}, reads: make(map[digest.Digest]int)}
	blob, err := exportLayer(t.Context(), layer{source: src, info: types.BlobInfo{Digest: d, MediaType: oci.MediaTypeImageLayerGzip}, descriptor: oci.Descriptor{Digest: digest.FromBytes(data), Size: -1}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blob.Close() }()
	got, err := io.ReadAll(io.NewSectionReader(blob.ReaderAt, 0, blob.Size))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("decompressed bytes: %v", err)
	}
}

type trackedReader struct {
	io.Reader
	closed int
}

func (r *trackedReader) Close() error { r.closed++; return nil }

type seekableReader struct {
	*bytes.Reader
	closed int
}

func (r *seekableReader) Close() error { r.closed++; return nil }
func TestSeekableExportReaderIsReused(t *testing.T) {
	data := []byte("seekable layer")
	reader := &seekableReader{Reader: bytes.NewReader(data)}
	blob, err := verifyReader(t.Context(), reader, digest.FromBytes(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if blob.ReaderAt != reader {
		t.Fatal("copied an already seekable export")
	}
	if err := blob.Close(); err != nil {
		t.Fatal(err)
	}
	if reader.closed != 1 {
		t.Fatal("reader not closed")
	}
}
func TestPinnedManifestCannotSwitchOnRetagOrDeletion(t *testing.T) {
	src, record := fixture(t, 0)
	request := newRequest(func(context.Context, string) (types.ImageSource, error) { return src, nil }, nil)
	// Simulate deletion/replacement after inventory resolution. A different default
	// must not be accepted under the old descriptor, even for the same storage ID.
	src.manifest = []byte(`{"schemaVersion":2,"mediaType":"replacement"}`)
	a := image.NewArtifact(record.Target)
	defer func() { _ = request.Close() }()
	if _, err := request.prepare(t.Context(), record.ID, record.Target, a, 0); err == nil {
		t.Fatal("served replacement manifest")
	}
	request = newRequest(func(context.Context, string) (types.ImageSource, error) { return nil, image.ErrNotFound }, nil)
	if _, err := request.prepare(t.Context(), record.ID, record.Target, a, 0); !errors.Is(err, image.ErrNotFound) {
		t.Fatalf("deletion: %v", err)
	}
}
