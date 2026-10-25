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
