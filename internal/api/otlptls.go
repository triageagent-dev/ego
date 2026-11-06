package api

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"
)

// OTLPTLS is the certificate pair of the OTLP receivers (4317 and 4318). Both empty keeps them plain, as before;
// both set serves TLS on both, so the sender bearer token does not cross the network in clear text.
type OTLPTLS struct {
	CertFile, KeyFile string
}

func (t OTLPTLS) enabled() bool { return t.CertFile != "" && t.KeyFile != "" }

// check refuses half a pair at start rather than serving plain text by mistake.
func (t OTLPTLS) check() error {
	if (t.CertFile == "") != (t.KeyFile == "") {
		return fmt.Errorf("--otlp-tls-cert and --otlp-tls-key go together")
	}
	return nil
}

// config returns the server TLS config, or nil when TLS is off. The pair is read again when the files change on disk,
// so a renewed certificate (cert-manager rewrites the mounted Secret) is served without a restart.
func (t OTLPTLS) config() (*tls.Config, error) {
	if !t.enabled() {
		return nil, nil
	}
	r := &certReloader{certFile: t.CertFile, keyFile: t.KeyFile}
	if _, err := r.get(nil); err != nil {
		return nil, fmt.Errorf("loading the OTLP TLS pair: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.get}, nil
}

type certReloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	loaded  time.Time // when the pair on disk was last checked
	modTime time.Time // modification time of the certificate file that was loaded
}

// recheckEvery bounds how often the files are stat'ed, so a handshake storm does not become a stat storm.
const recheckEvery = 30 * time.Second

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cert != nil && time.Since(r.loaded) < recheckEvery {
		return r.cert, nil
	}
	st, err := os.Stat(r.certFile)
	if err != nil {
		if r.cert != nil {
			r.loaded = time.Now() // keep serving the last good pair, and look again after recheckEvery
			return r.cert, nil
		}
		return nil, err
	}
	if r.cert != nil && st.ModTime().Equal(r.modTime) {
		r.loaded = time.Now()
		return r.cert, nil
	}
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cert != nil {
			r.loaded = time.Now() // a half-written renewal: keep the last good pair, try again after recheckEvery
			return r.cert, nil
		}
		return nil, err
	}
	r.cert, r.modTime, r.loaded = &c, st.ModTime(), time.Now()
	return r.cert, nil
}
