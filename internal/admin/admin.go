// Package admin serves an HTML viewer using templ and bundled UI assets.
package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"regview/internal/assets"
	"regview/internal/catalog"
	"regview/internal/image"
)

func New(c *catalog.Catalog, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := page(c.View(r.Context())).Render(r.Context(), w); err != nil {
			logger.Error("render admin page", "error", err)
		}
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets.FS())))
	mux.HandleFunc("GET /images", func(w http.ResponseWriter, r *http.Request) {
		view := c.View(r.Context())
		query := r.URL.Query().Get("q")
		if query != "" {
			rows := make([]catalog.Row, 0, len(view.Rows))
			for _, row := range view.Rows {
				if strings.Contains(row.Host+"/"+row.Repository+":"+row.Tag+" "+row.Backend, query) {
					rows = append(rows, row)
				}
			}
			view.Rows = rows
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := inventory(view).Render(r.Context(), w); err != nil {
			logger.Error("render inventory", "error", err)
		}
	})
	mux.HandleFunc("GET /manifest", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		manifest, err := c.Manifest(r.Context(), query.Get("host"), query.Get("repository"), query.Get("reference"))
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, image.ErrNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, "Manifest is unavailable", status)
			return
		}
		// Format the original JSON without converting numbers through float64.
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, manifest.Data, "", "  "); err != nil {
			http.Error(w, "Invalid manifest", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := manifestDetails(manifest.Descriptor.Digest.String(), pretty.String()).Render(r.Context(), w); err != nil {
			logger.Error("render manifest", "error", err)
		}
	})
	return mux
}
