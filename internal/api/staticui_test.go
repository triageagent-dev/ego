package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestUIFilesCachingAndCompression(t *testing.T) {
	bundle := strings.Repeat("console.log('ego');\n", 500)
	h := newUIFiles(fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>ego</title>")},
		"assets/app-1.js":  {Data: []byte(bundle)},
		"assets/app-1.css": {Data: []byte("a{color:red}")},
		"favicon.svg":      {Data: []byte(strings.Repeat("<svg></svg>", 200))},
	})
	get := func(path string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(rec.Body.String(), "<title>ego</title>") {
		t.Fatalf("index: %d %v", rec.Code, rec.Header())
	}

	rec = get("/assets/app-1.js", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("bundle headers = %v, want gzip and immutable", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(zr); string(body) != bundle {
		t.Fatal("gzipped bundle does not round-trip")
	}
	if rec.Body.Len() != 0 && rec.Header().Get("Content-Length") == "" {
		t.Fatal("missing Content-Length")
	}

	if rec = get("/assets/app-1.js", nil); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != bundle {
		t.Fatal("a client that does not accept gzip must get the plain file")
	}
	if rec = get("/assets/app-1.css", map[string]string{"Accept-Encoding": "gzip"}); rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("a tiny file is not worth compressing")
	}

	etag := get("/assets/app-1.js", nil).Header().Get("ETag")
	if rec = get("/assets/app-1.js", map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation = %d with %d bytes, want 304 and no body", rec.Code, rec.Body.Len())
	}

	for _, p := range []string{"/nope.js", "/../etc/passwd", "/assets/"} {
		if rec = get(p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}
