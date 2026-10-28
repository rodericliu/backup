package server

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// contentSecurityPolicy allows only same-origin resources. Element Plus sets
// inline styles, and ECharts renders images from data URLs.
const contentSecurityPolicy = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

const notBuilt = `<!doctype html><meta charset="utf-8"><title>backup-server</title>
<p>The web UI is not built into this binary. Run <code>make web</code> and rebuild backup-server.</p>`

// uiHandler serves the single-page application: existing files as they are
// (hashed assets cached for a year) and index.html for every other path so
// that client-side routes can be reloaded.
func (s *Server) uiHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		if strings.HasPrefix(p, "/api/") {
			s.writeError(w, r, notFound("API endpoint"))
			return
		}
		name := strings.TrimPrefix(p, "/")
		if fi, err := fs.Stat(fsys, name); err == nil && !fi.IsDir() && name != "index.html" {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Type", "text/html; charset=utf-8")
		b, err := fs.ReadFile(fsys, "index.html")
		if errors.Is(err, fs.ErrNotExist) {
			b = []byte(notBuilt)
		} else if err != nil {
			s.writeError(w, r, err)
			return
		}
		w.Write(b)
	})
}
