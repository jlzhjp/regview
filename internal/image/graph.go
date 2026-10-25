package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	digest "github.com/opencontainers/go-digest"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

// ReadGraph preserves original manifest bytes, including partially present indices.
// Missing children remain in the index and are reported separately.
func ReadGraph(ctx context.Context, root oci.Descriptor, read func(context.Context, oci.Descriptor) ([]byte, error)) (*Artifact, error) {
	a := NewArtifact(root)
	seen := make(map[digest.Digest]bool)
	var visit func(oci.Descriptor, int) error
	visit = func(desc oci.Descriptor, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 32 {
			return fmt.Errorf("image index nesting exceeds 32")
		}
		if seen[desc.Digest] {
			return nil
		}
		seen[desc.Digest] = true
		data, err := read(ctx, desc)
		if err != nil {
			if depth == 0 || !errors.Is(err, ErrNotFound) {
				return err
			}
			a.Missing = append(a.Missing, desc.Digest)
			return nil
		}
		if err := Verify(desc, data); err != nil {
			return err
		}
		a.Manifests[desc.Digest] = Manifest{Descriptor: desc, Data: data}
		switch {
		case IsIndex(desc.MediaType):
			var index oci.Index
			if err := json.Unmarshal(data, &index); err != nil {
				return err
			}
			for _, child := range index.Manifests {
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
		case IsManifest(desc.MediaType):
			var manifest oci.Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return err
			}
			a.Blobs[manifest.Config.Digest] = manifest.Config
			for _, layer := range manifest.Layers {
				a.Blobs[layer.Digest] = layer
			}
		default:
			return fmt.Errorf("unsupported manifest media type %q", desc.MediaType)
		}
		return nil
	}
	if err := visit(root, 0); err != nil {
		return nil, err
	}
	return a, nil
}

func Verify(desc oci.Descriptor, data []byte) error {
	if err := desc.Digest.Validate(); err != nil {
		return err
	}
	if !desc.Digest.Algorithm().Available() {
		return fmt.Errorf("unsupported digest algorithm %s", desc.Digest.Algorithm())
	}
	if int64(len(data)) != desc.Size || desc.Digest.Algorithm().FromBytes(data) != desc.Digest {
		return fmt.Errorf("content does not match descriptor %s", desc.Digest)
	}
	return nil
}
