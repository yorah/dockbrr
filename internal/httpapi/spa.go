package httpapi

import (
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// distFS is the production SPA filesystem (the embedded dist/ subtree).
var distFS = mustSub(embedded, "dist")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("httpapi: embed dist subtree: " + err.Error())
	}
	return sub
}

// NewSPAHandler serves the embedded SPA: a real file under dist/ is served with
// long-cache headers; any other path falls back to index.html (client-side
// routing). Paths under /api/ or == /healthz are never served the SPA; they
// 404 so the API router's own 404 semantics win for unmatched API routes.
func NewSPAHandler(dist fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Guard on the CLEANED path so a malformed "//api/x" (which cleans to
		// "/api/x") 404s instead of falling through to index.html.
		clean := path.Clean(r.URL.Path)
		if isAPIPath(clean) {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(clean, "/")
		if p != "" {
			if f, err := dist.Open(p); err == nil {
				defer f.Close()
				if st, serr := f.Stat(); serr == nil && !st.IsDir() {
					serveFile(w, p, f, st.Size(), true)
					return
				}
			}
		}
		serveIndex(w, dist)
	})
}

func isAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == "/api" || p == "/healthz"
}

func serveIndex(w http.ResponseWriter, dist fs.FS) {
	f, err := dist.Open("index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Cache-Control", "no-cache")
	var size int64
	if st != nil {
		size = st.Size()
	}
	serveFile(w, "index.html", f, size, false)
}

func serveFile(w http.ResponseWriter, name string, f fs.File, size int64, immutable bool) {
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		// Extensionless files (defensive, Vite assets always have an ext) get a
		// generic type rather than none, so the browser doesn't have to sniff.
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	if immutable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	// Advertise Content-Length when known so responses aren't chunked.
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
