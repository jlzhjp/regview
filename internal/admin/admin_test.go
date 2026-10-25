package admin

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"regview/internal/catalog"
	"regview/internal/image"
	"regview/internal/testutil"
)

func TestViewerAndHTMXPartials(t *testing.T) {
	b := testutil.NewBackend("docker")
	b.Add("example.test/team/api:v1", "layer")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := catalog.New([]image.Backend{b}, []string{"example.test"}, logger)
	h := New(c, logger)
	for _, tc := range []struct{ path, contains string }{
		{"/", `<script type="module" src="/assets/frontend/app.js"`},
		{"/", `<link rel="stylesheet" href="/assets/frontend/app.css"`},
		{"/", `<meta name="htmx-config" content='{"allowEval":false}'>`},
		{"/images", "team/api"},
		{"/images?q=nothing", "No images match"},
		{"/manifest?host=example.test&repository=team/api&reference=v1", "Served digest"},
		{"/assets/frontend/app.js", "htmx"},
		{"/assets/frontend/app.css", "color-scheme"},
		{"/assets/frontend/app.css", "url(/assets/frontend/IBM%20Plex%20Mono%20Var-Roman.woff2)"},
		{"/assets/frontend/IBM%20Plex%20Mono%20Var-Roman.woff2", "wOF2"},
		{"/assets/licenses.tar.gz", "\x1f\x8b"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(t.Context(), "GET", tc.path, nil)
		r.Header.Set("HX-Request", "true")
		h.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
		if tc.path == "/images" && strings.Contains(w.Body.String(), "<html") {
			t.Fatal("partial returned full page")
		}
	}
}

func TestManifestFormattingPreservesNumbers(t *testing.T) {
	backend := testutil.NewBackend("docker")
	manifest, _ := backend.Add("example.test/app:v1", "layer")
	// OCI permits extension fields. Formatting must not round a large integer
	// or normalize its numeric spelling when displaying the original manifest.
	data := strings.TrimSuffix(string(backend.Content[manifest.Digest]), "}") + `,"extension":{"integer":9007199254740993,"decimal":1.2300,"exponent":1e1000}}`
	target := backend.Put([]byte(data), manifest.MediaType)
	backend.Retag("example.test/app:v1", image.Record{ID: target.Digest.String(), Target: target})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(catalog.New([]image.Backend{backend}, []string{"example.test"}, logger), logger)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", "/manifest?host=example.test&repository=app&reference=v1", nil))
	if response.Code != 200 {
		t.Fatalf("manifest response: %d %s", response.Code, response.Body)
	}
	for _, number := range []string{"9007199254740993", "1.2300", "1e1000"} {
		if !strings.Contains(response.Body.String(), number) {
			t.Errorf("manifest display changed %s: %s", number, response.Body)
		}
	}
}
