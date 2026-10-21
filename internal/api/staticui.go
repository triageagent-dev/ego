package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// uiFiles serves the embedded UI. http.FileServer over an embed.FS sends no validators (embedded files have no
// modification time), so a browser re-downloaded the whole bundle on every load, uncompressed. Here each file is
// read once, gzipped once, and sent with an ETag; the build-hashed files under /assets/ are cacheable for good,
// and index.html, which names them, is revalidated on every load.
type uiFiles struct {
	fsys  fs.FS
	mu    sync.Mutex
	files map[string]*uiFile // nil value: the file does not exist
}

type uiFile struct {
	raw, gz     []byte
	etag, ctype string
}

func newUIFiles(fsys fs.FS) *uiFiles {
	return &uiFiles{fsys: fsys, files: map[string]*uiFile{}}
}

// minGzipBytes is the size under which compressing is not worth the header.
const minGzipBytes = 1024

func (u *uiFiles) load(name string) *uiFile {
	u.mu.Lock()
	defer u.mu.Unlock()
	if f, ok := u.files[name]; ok {
		return f
	}
	var f *uiFile
	if raw, err := fs.ReadFile(u.fsys, name); err == nil {
		sum := sha256.Sum256(raw)
		f = &uiFile{raw: raw, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: mime.TypeByExtension(path.Ext(name))}
		if f.ctype == "" {
			f.ctype = http.DetectContentType(raw)
		}
		if len(raw) >= minGzipBytes && compressible(f.ctype) {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = zw.Write(raw)
			if zw.Close() == nil && buf.Len() < len(raw) {
				f.gz = buf.Bytes()
			}
		}
	}
	u.files[name] = f
	return f
}

func compressible(ctype string) bool {
	return strings.HasPrefix(ctype, "text/") || strings.Contains(ctype, "javascript") ||
		strings.Contains(ctype, "json") || strings.Contains(ctype, "svg")
}

func (u *uiFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	f := u.load(name)
	if f == nil {
		http.NotFound(w, r)
		return
	}

	h := w.Header()
	h.Set("Content-Type", f.ctype)
	h.Set("ETag", f.etag)
	h.Add("Vary", "Accept-Encoding")
	if strings.HasPrefix(name, "assets/") {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body := f.raw
	if f.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = f.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}
