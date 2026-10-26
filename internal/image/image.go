// Package image defines the image data shared by storage, catalog, and HTTP layers.
package image

import (
	"context"
	"errors"
	"io"

	digest "github.com/opencontainers/go-digest"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

var ErrNotFound = errors.New("image content not found")

// Record pairs a backend image reference with its immutable local ID and target.
// Reference carries the repository name and optional tag or digest; Target is the
// pinned descriptor used for preparation, regardless of later tag changes.
type Record struct {
	Reference string
	ID        string
	Target    oci.Descriptor
}

type Manifest struct {
	Descriptor oci.Descriptor
	Data       []byte
}

// Artifact owns the metadata and content readers for a single request. Call Close
// even when only reading manifests: preparation can measure unknown-size layers.
type Artifact struct {
	Root      oci.Descriptor
	Manifests map[digest.Digest]Manifest
	Blobs     map[digest.Digest]oci.Descriptor
	Missing   []digest.Digest
	Content   Content
}

type Content interface {
	Open(context.Context, digest.Digest) (Blob, error)
	Close() error
}

func (a *Artifact) Close() error {
	if a.Content != nil {
		return a.Content.Close()
	}
	return nil
}

// BlobRef pins the request's image and defers reconstruction until Open. This
// lets HTTP HEAD and precondition checks use metadata without exporting a layer.
type BlobRef struct {
	Descriptor oci.Descriptor
	Artifact   *Artifact
}

func (b *BlobRef) Open(ctx context.Context) (Blob, error) {
	return b.Artifact.Content.Open(ctx, b.Descriptor.Digest)
}
func (b *BlobRef) Close() error { return b.Artifact.Close() }

type Blob struct {
	io.ReaderAt
	io.Closer
	Size int64
}

// Backend connections and verified immutable content may be shared, but records
// and prepared artifacts only live for the request that resolved them. Prepare
// must use the pinned ID and descriptor in Record rather than looking up its
// mutable tag again.
type Backend interface {
	Name() string
	List(context.Context) ([]Record, error)
	Prepare(context.Context, Record) (*Artifact, error)
	ResolveBlob(context.Context, Record, digest.Digest) (*BlobRef, error)
	Close() error
}

// FindBlob transfers the request artifact to the caller on success and releases
// it otherwise. Backends must only include content reachable from the record.
func FindBlob(a *Artifact, d digest.Digest) (*BlobRef, error) {
	if desc, ok := a.Blobs[d]; ok {
		return &BlobRef{Descriptor: desc, Artifact: a}, nil
	}
	if err := a.Close(); err != nil {
		return nil, err
	}
	return nil, ErrNotFound
}

func NewArtifact(root oci.Descriptor) *Artifact {
	return &Artifact{Root: root, Manifests: make(map[digest.Digest]Manifest), Blobs: make(map[digest.Digest]oci.Descriptor)}
}

const (
	DockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	DockerIndex    = "application/vnd.docker.distribution.manifest.list.v2+json"
)

func IsIndex(mediaType string) bool {
	return mediaType == oci.MediaTypeImageIndex || mediaType == DockerIndex
}

func IsManifest(mediaType string) bool {
	return mediaType == oci.MediaTypeImageManifest || mediaType == DockerManifest
}
