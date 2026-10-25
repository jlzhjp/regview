package podman

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	digest "github.com/opencontainers/go-digest"
	"regview/internal/image"
)

func gzipped(mediaType string) bool {
	return strings.HasSuffix(mediaType, "+gzip") || mediaType == "application/vnd.docker.image.rootfs.diff.tar.gzip"
}

func exportLayer(ctx context.Context, l layer) (image.Blob, error) {
	if err := ctx.Err(); err != nil {
		return image.Blob{}, err
	}
	r, _, err := l.source.GetBlob(ctx, l.info, nil)
	if err != nil {
		return image.Blob{}, storageError(err)
	}
	if gzipped(l.info.MediaType) {
		original := r
		gz, err := gzip.NewReader(r)
		if err != nil {
			_ = r.Close()
			return image.Blob{}, err
		}
		defer gz.Close()
		// Cancellation can safely close the underlying file while Read is in
		// progress; gzip.Reader.Close itself must not race with gzip.Reader.Read.
		r = &readCloser{Reader: gz, close: original.Close}
	}
	return verifyReader(ctx, r, l.descriptor.Digest, l.descriptor.Size)
}

// verifyReader reuses seekable official readers where possible. Otherwise it
// spools to an immediately unlinked file. No bytes are served before verification.
func verifyReader(ctx context.Context, r io.ReadCloser, expected digest.Digest, expectedSize int64) (result image.Blob, err error) {
	owner := &onceCloser{closer: r}
	stop := closeOnCancel(ctx, owner)
	defer func() {
		if err != nil {
			stop()
			_ = owner.Close()
		}
	}()
	if err = expected.Validate(); err != nil {
		return result, err
	}
	if !expected.Algorithm().Available() {
		return result, fmt.Errorf("unsupported digest algorithm %s", expected.Algorithm())
	}
	var readerAt io.ReaderAt
	var file *os.File
	var output io.Writer = io.Discard
	if seekable, ok := r.(interface {
		io.ReaderAt
		io.Seeker
	}); ok {
		if _, err = seekable.Seek(0, io.SeekStart); err != nil {
			return result, err
		}
		readerAt = seekable
	} else {
		file, err = os.CreateTemp("", "regview-layer-*")
		if err != nil {
			return result, err
		}
		// Linux keeps the descriptor usable after unlink, even on cancellation.
		if err = os.Remove(file.Name()); err != nil {
			_ = file.Close()
			return result, err
		}
		defer func() {
			if err != nil {
				_ = file.Close()
			}
		}()
		readerAt, output = file, file
	}
	h := expected.Algorithm().Digester()
	var size int64
	size, err = io.Copy(io.MultiWriter(output, h.Hash()), contextReader{ctx, r})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if h.Digest() != expected {
		return result, fmt.Errorf("export digest mismatch: expected %s, got %s", expected, h.Digest())
	}
	if expectedSize >= 0 && size != expectedSize {
		return result, fmt.Errorf("export size mismatch: expected %d, got %d", expectedSize, size)
	}
	if file != nil {
		stop()
		if err = owner.Close(); err != nil {
			return result, err
		}
		owner = &onceCloser{closer: file}
	}
	// Verification transfers ownership to the caller. The load context must not
	// close a successfully published cache entry when the load finishes.
	stop()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	return image.Blob{ReaderAt: readerAt, Size: size, Closer: owner}, nil
}

func closeOnCancel(ctx context.Context, owner io.Closer) func() bool {
	return context.AfterFunc(ctx, func() { _ = owner.Close() })
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type onceCloser struct {
	once   sync.Once
	closer io.Closer
	err    error
}

func (c *onceCloser) Close() error { c.once.Do(func() { c.err = c.closer.Close() }); return c.err }

type readCloser struct {
	io.Reader
	close func() error
}

func (r *readCloser) Close() error { return r.close() }
