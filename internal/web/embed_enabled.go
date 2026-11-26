//go:build embed_web

package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the embedded Vite SPA with history-API fallback to index.html.
// publicURL (an already validated origin, or "") replaces the origin
// placeholder in index.html so link-preview tags carry absolute URLs.
func Handler(publicURL string) http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("embed dist: " + err.Error())
	}
	rawIndex, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("embed dist: index.html: " + err.Error())
	}
	index := renderIndex(rawIndex, publicURL)
	fileServer := http.FileServer(http.FS(sub))

	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if cleanPath == "" || cleanPath == "index.html" {
			serveIndex(w)
			return
		}
		if f, err := sub.Open(cleanPath); err == nil {
			_ = f.Close()
			switch {
			case strings.HasPrefix(cleanPath, "assets/"):
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			case isBrandAsset(cleanPath):
				w.Header().Set("Cache-Control", "public, max-age=86400")
			}
			if strings.HasSuffix(cleanPath, ".webmanifest") {
				w.Header().Set("Content-Type", "application/manifest+json")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w)
	})
}

func isBrandAsset(p string) bool {
	switch path.Ext(p) {
	case ".png", ".ico", ".svg", ".webmanifest":
		return !strings.Contains(p, "/")
	}
	return false
}
