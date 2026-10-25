// Package docker reads Docker's containerd image namespace through its local API.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"regview/internal/image"
)

type Backend struct{ client *containerd.Client }

func New(ctx context.Context, engineSocket, socket, namespace string) (*Backend, error) {
	if socket == "" {
		address, ns, err := discover(ctx, engineSocket)
		if err != nil {
			return nil, err
		}
		socket = address
		if namespace == "" {
			namespace = ns
		}
	}
	if namespace == "" {
		namespace = "moby"
	}
	c, err := containerd.New(socket, containerd.WithDefaultNamespace(namespace), containerd.WithTimeout(5*time.Second))
	if err != nil {
		return nil, err
	}
	if _, err := c.Version(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("connect to Docker containerd at %s: %w", socket, err)
	}
	return &Backend{client: c}, nil
}

// Docker reports the actual containerd endpoint, including custom/rootless installs.
func discover(ctx context.Context, engineSocket string) (string, string, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", engineSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/info", nil)
	if err != nil {
		return "", "", err
	}
	response, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("discover containerd using Docker socket %s: %w", engineSocket, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return "", "", fmt.Errorf("docker info returned HTTP %d", response.StatusCode)
	}
	var info struct {
		DriverStatus [][2]string
		Containerd   struct {
			Address    string
			Namespaces struct{ Containers string }
		}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&info); err != nil {
		return "", "", err
	}
	for _, status := range info.DriverStatus {
		if status[0] == "driver-type" && status[1] == "io.containerd.snapshotter.v1" {
			if info.Containerd.Address == "" {
				return "", "", fmt.Errorf("docker did not report its containerd socket; set --docker-socket")
			}
			return info.Containerd.Address, info.Containerd.Namespaces.Containers, nil
		}
	}
	return "", "", fmt.Errorf("docker uses classic image storage; only the containerd image store is supported")
}

func (*Backend) Name() string   { return "docker" }
func (b *Backend) Close() error { return b.client.Close() }

func (b *Backend) List(ctx context.Context) ([]image.Record, error) {
	images, err := b.client.ImageService().List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]image.Record, 0, len(images))
	for _, img := range images {
		result = append(result, image.Record{Reference: img.Name, ID: img.Target.Digest.String(), Target: img.Target})
	}
	return result, nil
}

func (b *Backend) Prepare(ctx context.Context, record image.Record) (*image.Artifact, error) {
	a, err := image.ReadGraph(ctx, record.Target, func(ctx context.Context, desc oci.Descriptor) ([]byte, error) {
		if desc.Size < 0 || desc.Size > 8<<20 {
			return nil, fmt.Errorf("manifest size out of bounds: %d", desc.Size)
		}
		blob, err := b.Open(ctx, desc.Digest)
		if err != nil {
			return nil, err
		}
		defer func() { _ = blob.Close() }()
		return io.ReadAll(io.NewSectionReader(blob.ReaderAt, 0, desc.Size))
	})
	if err != nil {
		return nil, err
	}
	a.Content = content{backend: b}
	return a, nil
}

type content struct{ backend *Backend }

func (b *Backend) ResolveBlob(ctx context.Context, record image.Record, d digest.Digest) (*image.BlobRef, error) {
	a, err := b.Prepare(ctx, record)
	if err != nil {
		return nil, err
	}
	return image.FindBlob(a, d)
}

func (c content) Open(ctx context.Context, d digest.Digest) (image.Blob, error) {
	return c.backend.Open(ctx, d)
}
func (content) Close() error { return nil }

func (b *Backend) Open(ctx context.Context, d digest.Digest) (image.Blob, error) {
	r, err := b.client.ContentStore().ReaderAt(ctx, oci.Descriptor{Digest: d})
	if errdefs.IsNotFound(err) {
		return image.Blob{}, image.ErrNotFound
	}
	if err != nil {
		return image.Blob{}, err
	}
	return image.Blob{ReaderAt: r, Closer: r, Size: r.Size()}, nil
}
