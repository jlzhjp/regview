package podman

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"regview/internal/catalog"
	"regview/internal/image"
)

func cachedFixture(t *testing.T, sizes ...int64) (*fixtureBackend, *source, image.Record) {
	t.Helper()
	src, record := fixture(t, sizes...)
	record.Reference = "example.test/app:v1"
	cache, err := newBlobCache(BlobCacheOptions{TTL: time.Second, MaxBytes: 1 << 20, MaxEntries: 4}, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	b := &fixtureBackend{records: []image.Record{record}, sources: map[string]*source{record.ID: src}, cache: cache}
	t.Cleanup(func() { _ = b.Close() })
	return b, src, record
}

func openFixture(t *testing.T, ctx context.Context, b *fixtureBackend, record image.Record, src *source) (*image.BlobRef, image.Blob) {
	t.Helper()
	ref, err := b.ResolveBlob(ctx, record, src.layers[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := ref.Open(ctx)
	if err != nil {
		_ = ref.Close()
		t.Fatal(err)
	}
	return ref, blob
}

func TestCachedReaderSurvivesRequestAndFillCancellation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	b, src, record := cachedFixture(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	firstRef, first := openFixture(t, ctx, b, record, src)
	secondRef, second := openFixture(t, t.Context(), b, record, src)
	if first.ReaderAt != second.ReaderAt || src.readCount(src.layers[0].Digest) != 1 {
		t.Fatal("requests did not reuse verified reader")
	}
	cancel()
	_ = first.Close()
	_ = firstRef.Close()
	data, err := io.ReadAll(io.NewSectionReader(second.ReaderAt, 0, second.Size))
	if err != nil || len(data) != int(second.Size) {
		t.Fatalf("one request closed shared file: %v", err)
	}
	_ = b.Close() // Active lease must remain usable across cache shutdown.
	if _, err := second.ReaderAt.ReadAt(make([]byte, 1), 0); err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	_ = secondRef.Close()
	if _, err := second.ReaderAt.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("shared file not closed: %v", err)
	}
	assertNoTemporaryReaders(t, dir)
}

func TestUnknownSizeReusesVerifiedMeasurementAcrossRequests(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	b, src, record := cachedFixture(t, -1, 0)
	a, err := b.Prepare(t.Context(), record)
	if err != nil {
		t.Fatal(err)
	}
	root := a.Root
	_ = a.Close()
	a, err = b.Prepare(t.Context(), record)
	if err != nil {
		t.Fatal(err)
	}
	if a.Root.Digest != root.Digest || src.readCount(src.layers[0].Digest) != 1 || src.readCount(src.layers[1].Digest) != 0 {
		t.Fatal("unknown measurement re-exported or known layer exported")
	}
	_ = a.Close()
	_ = b.Close()
	assertNoTemporaryReaders(t, dir)
}

func TestTTLReconstructionKeepsExistingDownloadAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, src, record := cachedFixture(t, 0)
		oldRef, old := openFixture(t, t.Context(), b, record, src)
		time.Sleep(2 * time.Second)
		freshRef, fresh := openFixture(t, t.Context(), b, record, src)
		if old.ReaderAt == fresh.ReaderAt || src.readCount(src.layers[0].Digest) != 2 {
			t.Fatal("expired reader reused")
		}
		if _, err := old.ReaderAt.ReadAt(make([]byte, 1), 0); err != nil {
			t.Fatal("expiry interrupted active download")
		}
		_ = old.Close()
		_ = oldRef.Close()
		if _, err := old.ReaderAt.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
			t.Fatal("expired reader leaked")
		}
		_ = fresh.Close()
		_ = freshRef.Close()
		_ = b.Close()
	})
}

func TestMeasuredReaderRemainsPinnedWithinRequestAfterExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, src, record := cachedFixture(t, -1)
		ref, err := b.ResolveBlob(t.Context(), record, src.layers[0].Digest)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		blob, err := ref.Open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if src.readCount(src.layers[0].Digest) != 1 {
			t.Fatal("expired measurement reconstructed within its owning request")
		}
		if _, err := blob.ReaderAt.ReadAt(make([]byte, 1), 0); err != nil {
			t.Fatal(err)
		}
		_ = blob.Close()
		_ = ref.Close()
		_ = b.Close()
	})
}

func TestWarmReaderDoesNotAuthorizeDeletedOrOtherRepository(t *testing.T) {
	b, src, _ := cachedFixture(t, 0)
	c := catalog.New([]image.Backend{b}, []string{"example.test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d := src.layers[0].Digest
	ref, err := c.Blob(t.Context(), "example.test", "app", d)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := ref.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = blob.Close()
	_ = ref.Close()
	if _, err := c.Blob(t.Context(), "example.test", "other", d); !errors.Is(err, image.ErrNotFound) {
		t.Fatalf("cached digest escaped repository: %v", err)
	}
	if _, err := c.Blob(t.Context(), "wrong.test", "app", d); !errors.Is(err, image.ErrNotFound) {
		t.Fatalf("cached digest escaped host: %v", err)
	}
	b.records = nil
	if _, err := c.Blob(t.Context(), "example.test", "app", d); !errors.Is(err, image.ErrNotFound) {
		t.Fatalf("deleted image served from cache: %v", err)
	}
	if _, err := c.Manifest(t.Context(), "example.test", "app", "v1"); !errors.Is(err, image.ErrNotFound) {
		t.Fatalf("inventory was cached: %v", err)
	}
	if src.readCount(d) != 1 {
		t.Fatal("denied request reconstructed a blob")
	}
	_ = b.Close()
}

func TestInvalidExportIsNotCached(t *testing.T) {
	b, src, record := cachedFixture(t, 0)
	d := src.layers[0].Digest
	valid := src.blobs[d]
	src.blobs[d] = []byte("corrupt")
	ref, err := b.ResolveBlob(t.Context(), record, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ref.Open(t.Context()); err == nil {
		t.Fatal("invalid bytes admitted")
	}
	_ = ref.Close()
	src.blobs[d] = valid
	ref, blob := openFixture(t, t.Context(), b, record, src)
	if src.readCount(d) != 2 {
		t.Fatal("failed export was cached")
	}
	_ = blob.Close()
	_ = ref.Close()
	_ = b.Close()
}
