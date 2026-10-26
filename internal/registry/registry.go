// Package registry implements the read-only OCI/Docker Registry HTTP API.
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
	"regview/internal/catalog"
	"regview/internal/image"
)

type Handler struct {
	catalog *catalog.Catalog
	logger  *slog.Logger
}

func New(c *catalog.Catalog, logger *slog.Logger) http.Handler {
	h := &Handler{catalog: c, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/{$}", h.ping)
	mux.HandleFunc("/v2/{path...}", h.dispatch)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		failure(w, r, http.StatusNotFound, "NAME_UNKNOWN", "unknown registry endpoint")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			failure(w, r, http.StatusMethodNotAllowed, "UNSUPPORTED", "registry is read-only")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (*Handler) ping(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, struct{}{})
}

func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "_catalog" {
		h.repositories(w, r)
		return
	}
	if repo, ok := strings.CutSuffix(path, "/tags/list"); ok {
		if !validRepository(repo) {
			failure(w, r, 400, "NAME_INVALID", "invalid repository name")
			return
		}
		h.tags(w, r, repo)
		return
	}
	// These words are also valid repository path components. The rightmost
	// marker identifies the endpoint, regardless of the repository's name.
	kind, offset := "", -1
	for _, candidate := range []string{"manifests", "blobs"} {
		if i := strings.LastIndex(path, "/"+candidate+"/"); i > offset {
			kind, offset = candidate, i
		}
	}
	if offset >= 0 {
		repo, ref := path[:offset], path[offset+len(kind)+2:]
		if !validRepository(repo) {
			failure(w, r, 400, "NAME_INVALID", "invalid repository name")
			return
		}
		if ref == "" || strings.Contains(ref, "/") {
			failure(w, r, 400, "NAME_INVALID", "invalid content reference")
			return
		}
		if kind == "manifests" {
			h.manifest(w, r, repo, ref)
		} else {
			h.blob(w, r, repo, ref)
		}
		return
	}
	failure(w, r, 404, "NAME_UNKNOWN", "unknown registry endpoint")
}

func validRepository(repo string) bool {
	if repo == "" {
		return false
	}
	// WithName accepts an optional registry authority. Supply one explicitly so
	// every component of repo is validated as a path, never as a hostname or port.
	named, err := reference.WithName("registry.invalid/" + repo)
	return err == nil && reference.Path(named) == repo
}

func (h *Handler) repositories(w http.ResponseWriter, r *http.Request) {
	values, err := h.catalog.Repositories(r.Context(), r.Host)
	if err != nil {
		h.contentError(w, r, err, "NAME_UNKNOWN")
		return
	}
	values, err = paginate(w, r, values)
	if err != nil {
		failure(w, r, 400, "PAGINATION_NUMBER_INVALID", err.Error())
		return
	}
	writeJSON(w, r, 200, struct {
		Repositories []string `json:"repositories"`
	}{values})
}

func (h *Handler) tags(w http.ResponseWriter, r *http.Request, repo string) {
	values, err := h.catalog.Tags(r.Context(), r.Host, repo)
	if err != nil {
		h.contentError(w, r, err, "NAME_UNKNOWN")
		return
	}
	values, err = paginate(w, r, values)
	if err != nil {
		failure(w, r, 400, "PAGINATION_NUMBER_INVALID", err.Error())
		return
	}
	writeJSON(w, r, 200, struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}{repo, values})
}

func paginate(w http.ResponseWriter, r *http.Request, values []string) ([]string, error) {
	query := r.URL.Query()
	if last := query.Get("last"); last != "" {
		i, found := slices.BinarySearch(values, last)
		if found {
			i++
		}
		values = values[i:]
	}
	n := len(values)
	if raw, ok := query["n"]; ok {
		var err error
		if len(raw) != 1 {
			return nil, fmt.Errorf("n must be supplied once")
		}
		n, err = strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > 10000 {
			return nil, fmt.Errorf("n must be between 1 and 10000")
		}
	}
	if n < len(values) {
		next := url.URL{Path: r.URL.Path}
		query.Set("last", values[n-1])
		query.Set("n", strconv.Itoa(n))
		next.RawQuery = query.Encode()
		w.Header().Set("Link", "<"+next.String()+">; rel=\"next\"")
		values = values[:n]
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

func (h *Handler) manifest(w http.ResponseWriter, r *http.Request, repo, ref string) {
	manifest, err := h.catalog.Manifest(r.Context(), r.Host, repo, ref)
	if err != nil {
		h.contentError(w, r, err, "MANIFEST_UNKNOWN")
		return
	}
	if !accepts(r.Header.Values("Accept"), manifest.Descriptor.MediaType) {
		failure(w, r, http.StatusNotAcceptable, "MANIFEST_UNKNOWN", "requested manifest media type is unavailable")
		return
	}
	w.Header().Set("Content-Type", manifest.Descriptor.MediaType)
	w.Header().Set("Docker-Content-Digest", manifest.Descriptor.Digest.String())
	w.Header().Set("ETag", strconv.Quote(manifest.Descriptor.Digest.String()))
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(manifest.Data))
}

func accepts(headers []string, mediaType string) bool {
	if len(headers) == 0 {
		return true
	}
	for _, header := range headers {
		for part := range strings.SplitSeq(header, ",") {
			typ, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			if q, exists := params["q"]; exists {
				quality, err := strconv.ParseFloat(q, 64)
				if err != nil || quality <= 0 || quality > 1 {
					continue
				}
			}
			if typ == mediaType || typ == "*/*" || (typ == "application/*" && strings.HasPrefix(mediaType, "application/")) {
				return true
			}
		}
	}
	return false
}

func (h *Handler) blob(w http.ResponseWriter, r *http.Request, repo, ref string) {
	d, err := digest.Parse(ref)
	if err != nil {
		failure(w, r, 400, "DIGEST_INVALID", "invalid blob digest")
		return
	}
	resolved, err := h.catalog.Blob(r.Context(), repo, d)
	if err != nil {
		h.contentError(w, r, err, "BLOB_UNKNOWN")
		return
	}
	defer func() { _ = resolved.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", d.String())
	w.Header().Set("ETag", strconv.Quote(d.String()))
	if serveMetadata(w, r, resolved.Descriptor.Size) {
		return
	}
	blob, err := resolved.Open(r.Context())
	if err != nil {
		h.contentError(w, r, err, "BLOB_UNKNOWN")
		return
	}
	defer func() { _ = blob.Close() }()
	if blob.Size != resolved.Descriptor.Size {
		h.contentError(w, r, fmt.Errorf("blob size differs from manifest"), "BLOB_UNKNOWN")
		return
	}
	http.ServeContent(w, r, "", time.Time{}, io.NewSectionReader(blob.ReaderAt, 0, blob.Size))
}

// Let net/http evaluate the same preconditions used for the body response,
// using a HEAD probe that cannot read content. Successful GETs then open and
// verify the layer before committing response headers. Range evaluation stays
// with the real ServeContent call; Range is only defined for GET.
func serveMetadata(w http.ResponseWriter, r *http.Request, size int64) bool {
	probe := r.Clone(r.Context())
	probe.Method = http.MethodHead
	probe.Header.Del("Range")
	probe.Header.Del("If-Range")
	response := &metadataResponse{header: w.Header().Clone()}
	http.ServeContent(response, probe, "", time.Time{}, io.NewSectionReader(noContent{}, 0, size))
	if r.Method != http.MethodHead && response.status == http.StatusOK {
		return false
	}
	clear(w.Header())
	maps.Copy(w.Header(), response.header)
	w.WriteHeader(response.status)
	return true
}

type noContent struct{}

func (noContent) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }

type metadataResponse struct {
	header http.Header
	status int
}

func (w *metadataResponse) Header() http.Header { return w.header }
func (w *metadataResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *metadataResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}

func (h *Handler) contentError(w http.ResponseWriter, r *http.Request, err error, code string) {
	w.Header().Del("ETag")
	w.Header().Del("Docker-Content-Digest")
	if errors.Is(err, image.ErrNotFound) {
		failure(w, r, 404, code, "requested content is unavailable")
		return
	}
	h.logger.ErrorContext(r.Context(), "registry content request failed", "host", r.Host, "path", r.URL.Path, "error", err)
	failure(w, r, http.StatusServiceUnavailable, "UNKNOWN", "local image backend is unavailable")
}

func failure(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	type registryError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	writeJSON(w, r, status, struct {
		Errors []registryError `json:"errors"`
	}{[]registryError{{code, message}}})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "JSON encoding failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
