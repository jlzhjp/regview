package image_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"regview/internal/image"
	"regview/internal/testutil"
)

func TestIndexPreservesBytesAndTracksMissingChildren(t *testing.T) {
	b := testutil.NewBackend("docker")
	manifest, _ := b.Add("example.test/app:v1", "layer")
	missing := oci.Descriptor{MediaType: oci.MediaTypeImageManifest, Digest: digest.FromString("missing"), Size: 42}
	index := oci.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageIndex, Manifests: []oci.Descriptor{manifest, missing}}
	data, _ := json.Marshal(index)
	root := b.Put(data, oci.MediaTypeImageIndex)
	a, err := b.Prepare(t.Context(), image.Record{Target: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Missing) != 1 || a.Missing[0] != missing.Digest || len(a.Manifests) != 2 {
		t.Fatalf("bad graph: %+v", a)
	}
	if string(a.Manifests[root.Digest].Data) != string(data) {
		t.Fatal("index bytes changed")
	}
	b.Content[root.Digest] = []byte("corrupted manifest")
	if _, err := b.Prepare(t.Context(), image.Record{Target: root}); err == nil {
		t.Fatal("accepted corrupt manifest")
	}
}

func TestIndexPropagatesChildReadFailures(t *testing.T) {
	backend := testutil.NewBackend("docker")
	child, _ := backend.Add("example.test/app:v1", "layer")
	data, err := json.Marshal(oci.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageIndex, Manifests: []oci.Descriptor{child}})
	if err != nil {
		t.Fatal(err)
	}
	root := backend.Put(data, oci.MediaTypeImageIndex)
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("backend disconnected")} {
		_, err := image.ReadGraph(t.Context(), root, func(_ context.Context, desc oci.Descriptor) ([]byte, error) {
			if desc.Digest == child.Digest {
				return nil, failure
			}
			return data, nil
		})
		if !errors.Is(err, failure) {
			t.Fatalf("child failure %v was swallowed: %v", failure, err)
		}
	}
}
