// Package testutil supplies in-memory image stores for protocol tests.
package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"sync"
	"sync/atomic"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"regview/internal/image"
)

type Backend struct {
	mu            sync.RWMutex
	BackendName   string
	Records       []image.Record
	Content       map[digest.Digest][]byte
	ListError     error
	Lists         atomic.Int64
	Opens         atomic.Int64
	BeforePrepare func(image.Record)
}

func NewBackend(name string) *Backend {
	return &Backend{BackendName: name, Content: make(map[digest.Digest][]byte)}
}
func (b *Backend) Name() string { return b.BackendName }
func (*Backend) Close() error   { return nil }
func (b *Backend) List(context.Context) ([]image.Record, error) {
	b.Lists.Add(1)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return slices.Clone(b.Records), b.ListError
}
func (b *Backend) Prepare(ctx context.Context, record image.Record) (*image.Artifact, error) {
	if b.BeforePrepare != nil {
		b.BeforePrepare(record)
	}
	a, err := image.ReadGraph(ctx, record.Target, func(_ context.Context, d oci.Descriptor) ([]byte, error) {
		b.mu.RLock()
		defer b.mu.RUnlock()
		data, ok := b.Content[d.Digest]
		if !ok {
			return nil, image.ErrNotFound
		}
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	a.Content = b
	return a, nil
}
func (b *Backend) Open(_ context.Context, d digest.Digest) (image.Blob, error) {
	b.Opens.Add(1)
	b.mu.RLock()
	defer b.mu.RUnlock()
	data, ok := b.Content[d]
	if !ok {
		return image.Blob{}, image.ErrNotFound
	}
	return image.Blob{ReaderAt: bytes.NewReader(data), Closer: io.NopCloser(bytes.NewReader(nil)), Size: int64(len(data))}, nil
}

func (b *Backend) ResolveBlob(ctx context.Context, record image.Record, d digest.Digest) (*image.BlobRef, error) {
	a, err := b.Prepare(ctx, record)
	if err != nil {
		return nil, err
	}
	return image.FindBlob(a, d)
}

func (b *Backend) Add(ref, payload string) (manifest, layer oci.Descriptor) {
	config := b.Put([]byte(`{"architecture":"amd64","os":"linux"}`), oci.MediaTypeImageConfig)
	layer = b.Put([]byte(payload), oci.MediaTypeImageLayer)
	m := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageManifest, Config: config, Layers: []oci.Descriptor{layer}}
	data, _ := json.Marshal(m)
	manifest = b.Put(data, oci.MediaTypeImageManifest)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Records = append(b.Records, image.Record{Reference: ref, ID: manifest.Digest.String(), Target: manifest})
	return manifest, layer
}

func (b *Backend) Put(data []byte, mediaType string) oci.Descriptor {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := digest.FromBytes(data)
	b.Content[d] = data
	return oci.Descriptor{Digest: d, Size: int64(len(data)), MediaType: mediaType}
}

// Retag atomically changes a name without mutating previously listed records.
func (b *Backend) Retag(ref string, target image.Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Records = slices.DeleteFunc(b.Records, func(r image.Record) bool { return r.Reference == ref })
	target.Reference = ref
	b.Records = append(b.Records, target)
}
func (b *Backend) Remove(ref string, content digest.Digest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Records = slices.DeleteFunc(b.Records, func(r image.Record) bool { return r.Reference == ref })
	if content != "" {
		delete(b.Content, content)
	}
}
