// Package web serves the embedded admin UI (vanilla HTML/CSS/JS, no build
// step). Static assets need no auth — every data call goes to /admin/* which
// enforces the admin token itself.
package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed assets
var assetsFS embed.FS

var files, _ = fs.Sub(assetsFS, "assets")

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: blob: https: http:; " +
	"media-src 'self' data: blob: https: http:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'"

// Handler serves index.html at / and files under /assets/. Everything else
// (unknown non-API paths) is a 404.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			serve(w, r, "index.html")
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			serve(w, r, strings.TrimPrefix(r.URL.Path, "/assets/"))
		default:
			http.NotFound(w, r)
		}
	})
}

func serve(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	if strings.HasSuffix(name, ".html") {
		h.Set("Content-Security-Policy", csp)
	}
	w.Write(data)
}
