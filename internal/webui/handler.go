// Package webui serves the embedded single-page app: static files with the
// right cache and security headers, and an index.html fallback for client-side
// routes.
package webui

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// ContentSecurityPolicy is sent on every response from Handler.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; manifest-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const (
	indexFile    = "index.html"
	swFile       = "sw.js"
	assetsPrefix = "assets/"

	cacheNoCache   = "no-cache"
	cacheImmutable = "public, max-age=31536000, immutable"
)

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".webmanifest": "application/manifest+json",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".ico":         "image/x-icon",
	".webp":        "image/webp",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".map":         "application/json",
}

// Handler serves fsys. "/" and "/a/..." (push deep links) return index.html;
// any other path that is not a regular file is a 404, so a missing asset never
// turns into HTML. Directories are never listed. Only GET and HEAD are allowed.
func Handler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name, ok := resolve(r.URL.Path)
		if !ok {
			notFound(w)
			return
		}
		f, err := fsys.Open(name)
		if err != nil {
			notFound(w)
			return
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			notFound(w)
			return
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		switch {
		case name == swFile:
			h.Set("Cache-Control", cacheNoCache)
			h.Set("Service-Worker-Allowed", "/")
		case strings.HasPrefix(name, assetsPrefix):
			h.Set("Cache-Control", cacheImmutable)
		default: // index.html, manifest, icons
			h.Set("Cache-Control", cacheNoCache)
		}
		ct, ok := contentTypes[strings.ToLower(path.Ext(name))]
		if !ok {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		http.ServeContent(w, r, name, time.Time{}, rs)
	})
}

// resolve maps a request path to a file name inside the FS. Only names that
// pass fs.ValidPath are accepted (no "..", no empty or "." elements, no
// leading or trailing slash), so nothing outside the FS root is reachable.
func resolve(urlPath string) (string, bool) {
	if urlPath == "/" || urlPath == "/a" || strings.HasPrefix(urlPath, "/a/") {
		return indexFile, true
	}
	name := strings.TrimPrefix(urlPath, "/")
	if !fs.ValidPath(name) || name == "." {
		return "", false
	}
	return name, true
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", cacheNoCache)
	http.Error(w, "not found", http.StatusNotFound)
}
