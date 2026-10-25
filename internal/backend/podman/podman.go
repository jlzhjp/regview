// Package podman exports local storage images as OCI images with uncompressed layers.
package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	imageStorage "go.podman.io/image/v5/storage"
	"go.podman.io/image/v5/types"
	"go.podman.io/storage"
	"regview/internal/image"
)

type Options struct {
	GraphRoot, RunRoot, Driver string
	ExistingOnly               bool
	BlobCache                  BlobCacheOptions
	OnCacheError               func(error)
}

type Backend struct {
	store storage.Store
	blobs *blobCache
}

func New(options Options) (*Backend, error) {
	opts, err := ResolveOptions(options)
	if err != nil {
		return nil, err
	}
	if options.ExistingOnly {
		if err := ExistingStore(opts); err != nil {
			return nil, err
		}
	}
	blobs, err := newBlobCache(options.BlobCache, options.OnCacheError)
	if err != nil {
		return nil, fmt.Errorf("configure Podman blob cache: %w", err)
	}
	store, err := storage.GetStore(opts)
	if err != nil {
		_ = blobs.Close()
		return nil, fmt.Errorf("open Podman storage: %w", err)
	}
	return &Backend{store: store, blobs: blobs}, nil
}

func (*Backend) Name() string { return "podman" }

// Do not call storage.Shutdown: it can unmount layers used by the local engine.
func (b *Backend) Close() error { return b.blobs.Close() }

func (b *Backend) List(ctx context.Context) ([]image.Record, error) {
	images, err := b.store.Images()
	if err != nil {
		return nil, err
	}
	var result []image.Record
	for _, img := range images {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(img.Names) == 0 {
			continue
		}
		data, err := b.store.ImageBigData(img.ID, storage.ImageDigestBigDataKey)
		if err != nil {
			return nil, fmt.Errorf("read manifest for %s: %w", img.ID, err)
		}
		var header struct {
			MediaType string `json:"mediaType"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			return nil, err
		}
		desc := descriptor(data, header.MediaType)
		for _, name := range img.Names {
			result = append(result, image.Record{Reference: name, ID: img.ID, Target: desc})
		}
	}
	return result, nil
}

// Prepare only reads metadata, except when a layer's uncompressed size is
// unknown. Every source is opened by immutable storage ID, never by tag.
func (b *Backend) Prepare(ctx context.Context, record image.Record) (*image.Artifact, error) {
	return b.prepare(ctx, record, "")
}

func (b *Backend) ResolveBlob(ctx context.Context, record image.Record, d digest.Digest) (*image.BlobRef, error) {
	a, err := b.prepare(ctx, record, d)
	if err != nil {
		return nil, err
	}
	return image.FindBlob(a, d)
}

func (b *Backend) prepare(ctx context.Context, record image.Record, blob digest.Digest) (*image.Artifact, error) {
	s := newRequest(b.openImage, b.instanceID)
	s.cache = b.blobs
	s.wantedBlob = blob
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

func (b *Backend) openImage(ctx context.Context, id string) (types.ImageSource, error) {
	ref, err := imageStorage.Transport.NewStoreReference(b.store, nil, id)
	if err != nil {
		return nil, storageError(err)
	}
	source, err := ref.NewImageSource(ctx, nil)
	return source, storageError(err)
}
func (b *Backend) instanceID(_ context.Context, d digest.Digest) (string, error) {
	images, err := b.store.ImagesByDigest(d)
	if err != nil {
		return "", storageError(err)
	}
	if len(images) == 0 {
		return "", image.ErrNotFound
	}
	slices.SortFunc(images, func(x, y *storage.Image) int { return strings.Compare(x.ID, y.ID) })
	return images[0].ID, nil
}
func storageError(err error) error {
	if errors.Is(err, storage.ErrImageUnknown) || errors.Is(err, imageStorage.ErrNoSuchImage) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %v", image.ErrNotFound, err)
	}
	return err
}

type layer struct {
	reopen     func(context.Context) (types.ImageSource, error)
	source     types.ImageSource
	info       types.BlobInfo
	descriptor oci.Descriptor
}

// request owns generated metadata, sources, and measured-reader leases. It is
// never retained by Backend and must be closed by the HTTP request's owner.
type request struct {
	cache      *blobCache
	wantedBlob digest.Digest // empty for manifest generation
	openImage  func(context.Context, string) (types.ImageSource, error)
	instanceID func(context.Context, digest.Digest) (string, error)
	sources    []types.ImageSource
	configs    map[digest.Digest][]byte
	layers     map[digest.Digest]layer
	measured   map[digest.Digest]image.Blob
	prepared   map[string]oci.Descriptor
}

func newRequest(open func(context.Context, string) (types.ImageSource, error), instance func(context.Context, digest.Digest) (string, error)) *request {
	return &request{openImage: open, instanceID: instance, configs: make(map[digest.Digest][]byte), layers: make(map[digest.Digest]layer), measured: make(map[digest.Digest]image.Blob), prepared: make(map[string]oci.Descriptor)}
}
func (s *request) Close() error {
	var failures []error
	for _, blob := range s.measured {
		failures = append(failures, blob.Close())
	}
	for _, source := range s.sources {
		failures = append(failures, source.Close())
	}
	return errors.Join(failures...)
}

func (s *request) prepare(ctx context.Context, id string, expected oci.Descriptor, a *image.Artifact, depth int) (oci.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return oci.Descriptor{}, err
	}
	if depth > 32 {
		return oci.Descriptor{}, fmt.Errorf("image index nesting exceeds 32")
	}
	key := id + ":" + expected.Digest.String()
	if desc, ok := s.prepared[key]; ok {
		return desc, nil
	}
	source, data, mediaType, instance, err := pinnedSource(ctx, s.openImage, id, expected)
	if err != nil {
		return oci.Descriptor{}, err
	}
	s.sources = append(s.sources, source)
	var root oci.Descriptor
	if image.IsIndex(mediaType) {
		var index oci.Index
		if err := json.Unmarshal(data, &index); err != nil {
			return oci.Descriptor{}, err
		}
		children := make([]oci.Descriptor, 0, len(index.Manifests))
		for _, child := range index.Manifests {
			id, err := s.instanceID(ctx, child.Digest)
			if errors.Is(err, image.ErrNotFound) {
				a.Missing = append(a.Missing, child.Digest)
				continue
			}
			if err != nil {
				return oci.Descriptor{}, err
			}
			converted, err := s.prepare(ctx, id, child, a, depth+1)
			if err != nil {
				return oci.Descriptor{}, err
			}
			converted.Platform, converted.Annotations = child.Platform, child.Annotations
			children = append(children, converted)
		}
		if len(children) == 0 {
			return oci.Descriptor{}, fmt.Errorf("no local instances of Podman index %s: %w", expected.Digest, image.ErrNotFound)
		}
		index.MediaType, index.Manifests, index.Subject = oci.MediaTypeImageIndex, children, nil
		if s.wantedBlob == "" {
			root, err = addManifest(a, index, oci.MediaTypeImageIndex)
		} else {
			root = expected
		}
	} else {
		open := s.openImage
		reopen := func(ctx context.Context) (types.ImageSource, error) {
			source, _, _, instance, err := pinnedSource(ctx, open, id, expected)
			if err != nil {
				return nil, err
			}
			if _, err := source.LayerInfosForCopy(ctx, instance); err != nil {
				_ = source.Close()
				return nil, storageError(err)
			}
			return source, nil
		}
		root, err = s.prepareImage(ctx, source, data, mediaType, instance, reopen, a)
	}
	if err != nil {
		return oci.Descriptor{}, err
	}
	s.prepared[key] = root
	return root, nil
}

func (s *request) prepareImage(ctx context.Context, source types.ImageSource, data []byte, mediaType string, instance *digest.Digest, reopen func(context.Context) (types.ImageSource, error), a *image.Artifact) (oci.Descriptor, error) {
	if !image.IsManifest(mediaType) {
		return oci.Descriptor{}, fmt.Errorf("unsupported Podman manifest %q; OCI and Docker schema 2 are supported", mediaType)
	}
	var original oci.Manifest
	if err := json.Unmarshal(data, &original); err != nil {
		return oci.Descriptor{}, err
	}
	configData, err := readConfig(ctx, source, original.Config)
	if err != nil {
		return oci.Descriptor{}, err
	}
	var config oci.Image
	if err := json.Unmarshal(configData, &config); err != nil {
		return oci.Descriptor{}, err
	}
	infos, err := source.LayerInfosForCopy(ctx, instance)
	if err != nil {
		return oci.Descriptor{}, storageError(err)
	}
	if config.RootFS.Type != "layers" || len(infos) != len(config.RootFS.DiffIDs) {
		return oci.Descriptor{}, fmt.Errorf("layer metadata differs from config DiffIDs")
	}
	configDesc := original.Config
	configDesc.MediaType = oci.MediaTypeImageConfig
	s.configs[configDesc.Digest] = configData
	a.Blobs[configDesc.Digest] = configDesc
	layers := make([]oci.Descriptor, 0, len(infos))
	for i, info := range infos {
		if err := ctx.Err(); err != nil {
			return oci.Descriptor{}, err
		}
		expected := config.RootFS.DiffIDs[i]
		if err := expected.Validate(); err != nil {
			return oci.Descriptor{}, err
		}
		size := info.Size
		if gzipped(info.MediaType) {
			size = -1
		} else if info.Digest != expected {
			return oci.Descriptor{}, fmt.Errorf("layer metadata digest differs from config DiffID")
		}
		desc := oci.Descriptor{Digest: expected, Size: size, MediaType: oci.MediaTypeImageLayer}
		l := layer{source: source, info: info, descriptor: desc, reopen: reopen}
		if size < 0 && (s.wantedBlob == "" || s.wantedBlob == expected) {
			blob, exists := s.measured[expected]
			if !exists {
				blob, err = s.readLayer(ctx, l)
				if err != nil {
					return oci.Descriptor{}, fmt.Errorf("measure layer %d: %w", i, err)
				}
				s.measured[expected] = blob
			}
			desc.Size = blob.Size
			l.descriptor = desc
		}
		s.layers[expected] = l
		a.Blobs[expected] = desc
		layers = append(layers, desc)
	}
	converted := oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageManifest, Config: configDesc, Layers: layers, Annotations: original.Annotations}
	if s.wantedBlob != "" {
		return descriptor(data, mediaType), nil
	}
	return addManifest(a, converted, oci.MediaTypeImageManifest)
}

func (s *request) Open(ctx context.Context, d digest.Digest) (image.Blob, error) {
	if err := ctx.Err(); err != nil {
		return image.Blob{}, err
	}
	if data, ok := s.configs[d]; ok {
		return image.Blob{ReaderAt: bytes.NewReader(data), Closer: io.NopCloser(bytes.NewReader(nil)), Size: int64(len(data))}, nil
	}
	if blob, ok := s.measured[d]; ok {
		// The request retains this reader until Close, even if its cache entry
		// expires meanwhile. The body borrows it without releasing that owner.
		return image.Blob{ReaderAt: blob.ReaderAt, Size: blob.Size, Closer: io.NopCloser(bytes.NewReader(nil))}, nil
	}
	l, ok := s.layers[d]
	if !ok {
		return image.Blob{}, image.ErrNotFound
	}
	return s.readLayer(ctx, l)
}

// pinnedSource never resolves a mutable tag and owns cleanup on failed opens.
func pinnedSource(ctx context.Context, open func(context.Context, string) (types.ImageSource, error), id string, expected oci.Descriptor) (source types.ImageSource, data []byte, mediaType string, instance *digest.Digest, err error) {
	source, err = open(ctx, id)
	if err != nil {
		return nil, nil, "", nil, err
	}
	defer func() {
		if err != nil {
			_ = source.Close()
		}
	}()
	data, mediaType, err = source.GetManifest(ctx, nil)
	if err != nil {
		return source, nil, "", nil, storageError(err)
	}
	if digest.FromBytes(data) != expected.Digest {
		data, mediaType, err = source.GetManifest(ctx, &expected.Digest)
		if err != nil {
			return source, nil, "", nil, storageError(err)
		}
		instance = &expected.Digest
	}
	if err = image.Verify(expected, data); err != nil {
		return source, nil, "", nil, err
	}
	return source, data, mediaType, instance, nil
}

func readConfig(ctx context.Context, source types.ImageSource, desc oci.Descriptor) ([]byte, error) {
	if desc.Size < 0 || desc.Size > 8<<20 {
		return nil, fmt.Errorf("config size out of bounds")
	}
	r, _, err := source.GetBlob(ctx, types.BlobInfo{Digest: desc.Digest, Size: desc.Size}, nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, 8<<20+1))
	if err != nil {
		return nil, err
	}
	if err := image.Verify(desc, data); err != nil {
		return nil, err
	}
	return data, nil
}

func addManifest(a *image.Artifact, value any, mediaType string) (oci.Descriptor, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return oci.Descriptor{}, err
	}
	desc := descriptor(data, mediaType)
	a.Manifests[desc.Digest] = image.Manifest{Descriptor: desc, Data: data}
	return desc, nil
}
func descriptor(data []byte, mediaType string) oci.Descriptor {
	return oci.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
}
