package podman

import (
	"context"
	"fmt"
	"time"

	digest "github.com/opencontainers/go-digest"
	"regview/internal/image"
	"regview/internal/ttlcache"
)

type BlobCacheOptions struct {
	TTL        time.Duration
	MaxBytes   int64
	MaxEntries int
}

// Each backend owns its cache, so digest keys are scoped to that storage store.
// Only verified, uncompressed readers enter it; it never authorizes repository
// access or retains inventories, tags, or request-bound ImageSource objects.
type blobCache struct {
	entries *ttlcache.Cache[digest.Digest, image.Blob]
}

func newBlobCache(options BlobCacheOptions, onError func(error)) (*blobCache, error) {
	if options.TTL == 0 {
		return nil, nil
	}
	entries, err := ttlcache.New[digest.Digest](ttlcache.Options[image.Blob]{
		TTL:         options.TTL,
		LoadTimeout: 5 * time.Minute,
		MaxEntries:  options.MaxEntries,
		MaxWeight:   options.MaxBytes,
		Weight:      func(blob image.Blob) int64 { return blob.Size },
		Dispose:     func(blob image.Blob) error { return blob.Close() },
		OnError:     onError,
	})
	if err != nil {
		return nil, err
	}
	return &blobCache{entries: entries}, nil
}

func (c *blobCache) Close() error {
	if c == nil {
		return nil
	}
	return c.entries.Close()
}

func (c *blobCache) acquire(ctx context.Context, l layer) (image.Blob, error) {
	lease, err := c.entries.Acquire(ctx, l.descriptor.Digest, func(loadCtx context.Context) (image.Blob, error) {
		// A shared fill can outlive its first request. Open its own source by
		// pinned ID instead of capturing the request's source and cancellation.
		source, err := l.reopen(loadCtx)
		if err != nil {
			return image.Blob{}, err
		}
		defer func() { _ = source.Close() }()
		return exportLayer(loadCtx, layer{source: source, info: l.info, descriptor: l.descriptor})
	})
	if err != nil {
		return image.Blob{}, err
	}
	blob := lease.Value()
	if l.descriptor.Size >= 0 && blob.Size != l.descriptor.Size {
		_ = lease.Close()
		return image.Blob{}, fmt.Errorf("cached blob size differs from current descriptor")
	}
	return image.Blob{ReaderAt: blob.ReaderAt, Size: blob.Size, Closer: lease}, nil
}

func (s *request) readLayer(ctx context.Context, l layer) (image.Blob, error) {
	var blob image.Blob
	var err error
	if s.cache != nil {
		blob, err = s.cache.acquire(ctx, l)
	} else {
		blob, err = exportLayer(ctx, l)
	}
	if err != nil {
		return image.Blob{}, err
	}
	// Cancellation releases this request's lease, never the shared file owner.
	stop := closeOnCancel(ctx, blob.Closer)
	return image.Blob{ReaderAt: blob.ReaderAt, Size: blob.Size, Closer: &readCloser{close: func() error { stop(); return blob.Close() }}}, nil
}
