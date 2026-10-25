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
		{"/", `<script src="/assets/htmx.min.js" defer`},
		{"/images", "team/api"},
		{"/images?q=nothing", "No images match"},
		{"/manifest?host=example.test&repository=team/api&reference=v1", "Served digest"},
		{"/assets/htmx.min.js", "htmx"},
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
