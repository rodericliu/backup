package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestUI(t *testing.T) {
	s := &Server{}
	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<html>app</html>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"favicon.ico":      {Data: []byte("ico")},
		"assets/style.css": {Data: []byte("body{}")},
	}
	h := s.uiHandler(ui)
	get := func(path string) *http.Response {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Result()
	}
	body := func(r *http.Response) string {
		b, _ := io.ReadAll(r.Body)
		return string(b)
	}

	for _, p := range []string{"/", "/hosts", "/jobs/0190a/detail", "/index.html"} {
		r := get(p)
		if r.StatusCode != 200 || body(r) != "<html>app</html>" || !strings.Contains(r.Header.Get("Content-Security-Policy"), "default-src 'self'") ||
			r.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("GET %s = %d %v", p, r.StatusCode, r.Header)
		}
	}
	r := get("/assets/app-1.js")
	if r.StatusCode != 200 || body(r) != "console.log(1)" || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset = %d %v", r.StatusCode, r.Header)
	}
	if r := get("/favicon.ico"); r.StatusCode != 200 || strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("favicon = %d %v", r.StatusCode, r.Header)
	}
	if r := get("/api/v1/nope"); r.StatusCode != 404 || !strings.Contains(body(r), "NOT_FOUND") {
		t.Errorf("unknown API = %d", r.StatusCode)
	}
	if r := get("/../../etc/passwd"); body(r) != "<html>app</html>" {
		t.Errorf("path traversal served %q", body(r))
	}

	empty := s.uiHandler(fstest.MapFS{})
	rec := httptest.NewRecorder()
	empty.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("without UI = %q", rec.Body.String())
	}
}
