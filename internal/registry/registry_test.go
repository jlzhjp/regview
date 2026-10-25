package registry_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"regview/internal/catalog"
	"regview/internal/image"
	"regview/internal/registry"
	"regview/internal/testutil"
)

func TestRegistryPullAndIsolation(t *testing.T) {
	docker := testutil.NewBackend("docker")
	manifest, layer := docker.Add("example.test:5000/team/nested/api:latest", "0123456789")
	docker.Add("example.test:5000/team/nested/api:v2", "second version")
	_, privateLayer := docker.Add("private.test/team/nested/api:latest", "secret")
	_, otherLayer := docker.Add("example.test:5000/other:latest", "other repository")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := catalog.New([]image.Backend{docker}, []string{"example.test:5000", "private.test"}, logger)
	handler := registry.New(c, logger)
	request := func(method, host, path string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), method, "http://"+host+path, nil)
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, host := range []string{"example.test", "example.test:5001", "EXAMPLE.test:5000"} {
		if w := request("GET", host, "/v2/", nil); w.Code != 404 {
			t.Fatalf("host %s: %d", host, w.Code)
		}
	}
	if w := request("GET", "example.test:5000", "/v2/", nil); w.Code != 200 || w.Body.String() != "{}" {
		t.Fatalf("ping: %d %s", w.Code, w.Body)
	}
	path := "/v2/team/nested/api/manifests/latest"
	get := request("GET", "example.test:5000", path, nil)
	if get.Code != 200 || !bytes.Equal(get.Body.Bytes(), docker.Content[manifest.Digest]) {
		t.Fatalf("manifest: %d %s", get.Code, get.Body)
	}
	if got := get.Header().Get("Docker-Content-Digest"); got != manifest.Digest.String() {
		t.Fatalf("digest: %s", got)
	}
	head := request("HEAD", "example.test:5000", path, nil)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header())
	}
	if w := request("GET", "example.test:5000", path, map[string]string{"Accept": "application/vnd.oci.image.manifest.v1+json;q=0"}); w.Code != 406 {
		t.Fatalf("accept: %d", w.Code)
	}
	if w := request("GET", "example.test:5000", path, map[string]string{"If-None-Match": get.Header().Get("ETag")}); w.Code != 304 {
		t.Fatalf("conditional: %d", w.Code)
	}
	byDigest := "/v2/team/nested/api/manifests/" + manifest.Digest.String()
	if w := request("GET", "example.test:5000", byDigest, nil); w.Code != 200 {
		t.Fatalf("digest manifest: %d", w.Code)
	}
	blobPath := "/v2/team/nested/api/blobs/" + layer.Digest.String()
	for _, tc := range []struct {
		method  string
		headers map[string]string
		status  int
	}{
		{"HEAD", nil, 200},
		{"GET", map[string]string{"If-None-Match": "*"}, 304},
		{"GET", map[string]string{"If-None-Match": "W/\"" + layer.Digest.String() + "\""}, 304},
		{"GET", map[string]string{"If-Match": "\"wrong\""}, 412},
	} {
		before := docker.Opens.Load()
		w := request(tc.method, "example.test:5000", blobPath, tc.headers)
		if w.Code != tc.status || w.Body.Len() != 0 {
			t.Fatalf("metadata response: %d %s", w.Code, w.Body)
		}
		if docker.Opens.Load() != before {
			t.Fatal("HEAD/conditional opened blob")
		}
	}
	if w := request("GET", "example.test:5000", blobPath, map[string]string{"Range": "bytes=2-5"}); w.Code != 206 || w.Body.String() != "2345" {
		t.Fatalf("range: %d %s", w.Code, w.Body)
	}
	if w := request("GET", "example.test:5000", blobPath, map[string]string{"Range": "bytes=2-5", "If-Range": "\"wrong\""}); w.Code != 200 || w.Body.String() != "0123456789" {
		t.Fatalf("If-Range mismatch: %d %s", w.Code, w.Body)
	}
	if w := request("GET", "example.test:5000", blobPath, map[string]string{"Range": "bytes=999-1000"}); w.Code != 416 {
		t.Fatalf("invalid range: %d", w.Code)
	}
	for _, d := range []string{privateLayer.Digest.String(), otherLayer.Digest.String()} {
		if w := request("GET", "example.test:5000", "/v2/team/nested/api/blobs/"+d, nil); w.Code != 404 {
			t.Fatalf("leaked blob %s: %d", d, w.Code)
		}
	}
	if w := request("GET", "example.test:5000", "/v2/team/nested/api/blobs/sha256:invalid", nil); w.Code != 400 {
		t.Fatalf("digest validation: %d", w.Code)
	}
	for _, method := range []string{"PUT", "POST", "PATCH", "DELETE"} {
		w := request(method, "example.test:5000", path, nil)
		if w.Code != 405 || !strings.Contains(w.Body.String(), "UNSUPPORTED") {
			t.Fatalf("write %s: %d %s", method, w.Code, w.Body)
		}
	}
	tags := request("GET", "example.test:5000", "/v2/team/nested/api/tags/list?n=1", nil)
	if tags.Code != 200 || !strings.Contains(tags.Body.String(), `"tags":["latest"]`) || tags.Header().Get("Link") == "" {
		t.Fatalf("pagination: %d %s %v", tags.Code, tags.Body, tags.Header())
	}
	if w := request("GET", "example.test:5000", "/v2/team/nested/api/tags/list?n=0", nil); w.Code != 400 {
		t.Fatalf("bad pagination: %d", w.Code)
	}
	var result struct {
		Repositories []string `json:"repositories"`
	}
	w := request("GET", "example.test:5000", "/v2/_catalog", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 {
		t.Fatalf("catalog leaked hosts: %s", w.Body)
	}
}

func TestRepositoryNamesContainingEndpointSegments(t *testing.T) {
	for _, repo := range []string{"team/manifests/api", "team/blobs/api", "team/manifests/blobs/api", "team/blobs/manifests/api"} {
		t.Run(repo, func(t *testing.T) {
			backend := testutil.NewBackend("docker")
			manifest, layer := backend.Add("example.test/"+repo+":latest", "layer content")
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			handler := registry.New(catalog.New([]image.Backend{backend}, []string{"example.test"}, logger), logger)
			for path, want := range map[string]string{
				"/v2/" + repo + "/manifests/latest":               string(backend.Content[manifest.Digest]),
				"/v2/" + repo + "/blobs/" + layer.Digest.String(): "layer content",
			} {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "http://example.test"+path, nil))
				if w.Code != 200 || w.Body.String() != want {
					t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body)
				}
			}
		})
	}
}

func TestConflictAlwaysSelectsDocker(t *testing.T) {
	docker, podman := testutil.NewBackend("docker"), testutil.NewBackend("podman")
	dockerManifest, _ := docker.Add("example.test/app:v1", "docker")
	podman.Add("example.test/app:v1", "podman")
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	c := catalog.New([]image.Backend{podman, docker}, []string{"example.test"}, logger)
	manifest, err := c.Manifest(t.Context(), "example.test", "app", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Descriptor.Digest != dockerManifest.Digest {
		t.Fatal("Podman won conflicting tag")
	}
	if !strings.Contains(logs.String(), "WARN") || !strings.Contains(logs.String(), "conflicting image tag") {
		t.Fatalf("missing warning: %s", &logs)
	}
	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 10 {
				_, _ = c.Manifest(t.Context(), "example.test", "app", "v1")
				_ = c.View(t.Context())
			}
		})
	}
	wg.Wait()
}
