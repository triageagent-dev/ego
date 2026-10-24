package api

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/triageagent-dev/ego/internal/logx"
)

// The OTLP receivers are service-to-service pushes, so they cannot use the UI's cookie. A sender presents a
// bearer token (the OTLP exporters' `headers` setting). Two kinds are accepted:
//
//   - a sender token, minted by an admin for one sender: the spans it sends belong to that sender, and the tenants
//     it labels them with live in that sender's own namespace (see tenantKey);
//   - the shared --otlp-token: a trusted sender with no name, whose tenant labels are used as they are.
//
// With neither configured the receivers refuse every push until an admin sets --otlp-token or mints a sender token.
type ingestAuth struct {
	globalToken string
	senders     *SenderStore

	rejectMu   sync.Mutex
	rejectLast time.Time
	rejected   int

	failMu sync.Mutex
	fails  map[string]failWindow
}

// failWindow counts the refused pushes of one source in the current minute.
type failWindow struct {
	start time.Time
	n     int
}

const (
	// failLimit refused pushes in one failWindowLen from one source make the next ones answer "slow down" instead of
	// "unauthorized"; failTracked bounds the table of sources.
	failLimit     = 30
	failWindowLen = time.Minute
	failTracked   = 4096
)

// recordFailure counts a refused push from source and reports that the source is over its limit.
func (a *ingestAuth) recordFailure(source string) bool {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	now := time.Now()
	if a.fails == nil {
		a.fails = map[string]failWindow{}
	}
	w := a.fails[source]
	if now.Sub(w.start) > failWindowLen {
		w = failWindow{start: now}
	}
	w.n++
	if len(a.fails) >= failTracked {
		for k, v := range a.fails {
			if now.Sub(v.start) > failWindowLen {
				delete(a.fails, k)
			}
		}
		if len(a.fails) >= failTracked {
			clear(a.fails)
		}
	}
	a.fails[source] = w
	return w.n > failLimit
}

// sourceOf is the host part of a remote address.
func sourceOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// rejectLogEvery bounds the refusal log: a sender with a wrong token retries on every export.
const rejectLogEvery = time.Minute

// logRejected notes a refused push, at most once a minute with the count since the last note. It says why but
// never prints the token a sender presented.
func (a *ingestAuth) logRejected(transport, header string) {
	a.rejectMu.Lock()
	defer a.rejectMu.Unlock()
	a.rejected++
	if time.Since(a.rejectLast) < rejectLogEvery {
		return
	}
	reason := "unknown token"
	if _, has := bearerToken(header); !has {
		reason = "no bearer token"
	}
	logx.Warnf("ingest: refused %d OTLP/%s push(es), latest: %s", a.rejected, transport, reason)
	a.rejectLast, a.rejected = time.Now(), 0
}

// required reports whether pushes are checked. Every served receiver is: with no token configured and no sender
// token minted yet, it refuses every push, because an open receiver lets anyone who can reach it write sessions,
// scores and usage. Only a nil gate, the in-process setup of some tests, skips the check.
func (a *ingestAuth) required() bool {
	return a != nil
}

// configured reports whether any token can be presented: the shared one or a sender's.
func (a *ingestAuth) configured() bool {
	return a != nil && (a.globalToken != "" || a.senders.HasTokens())
}

func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(header[len(prefix):]), true
}

func bearerMatches(header, token string) bool {
	got, ok := bearerToken(header)
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// resolve returns the sender a request authenticates as ("" for the shared token or an open receiver), the tenant
// its token is bound to ("" when none) and whether it may send at all.
func (a *ingestAuth) resolve(header string) (sender, tenant string, ok bool) {
	if !a.required() {
		return "", "", true
	}
	got, has := bearerToken(header)
	if !has {
		return "", "", false
	}
	if a.globalToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(a.globalToken)) == 1 {
		return "", "", true
	}
	tok, ok := a.senders.Resolve(got)
	return tok.Sender, tok.Tenant, ok
}

type ingestSenderKey struct{}

// ingestPrincipal is who a push authenticated as: the sender, and the tenant its token is bound to.
type ingestPrincipal struct{ sender, tenant string }

func withIngestSender(ctx context.Context, sender, tenant string) context.Context {
	return context.WithValue(ctx, ingestSenderKey{}, ingestPrincipal{sender, tenant})
}

// ingestSender is the sender the receiver authenticated the sender as ("" when none), and ingestTenant the tenant
// its token is bound to ("" when none).
func ingestSender(ctx context.Context) string {
	p, _ := ctx.Value(ingestSenderKey{}).(ingestPrincipal)
	return p.sender
}

func ingestTenant(ctx context.Context) string {
	p, _ := ctx.Value(ingestSenderKey{}).(ingestPrincipal)
	return p.tenant
}

// middleware wraps an OTLP/HTTP handler.
func (a *ingestAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sender, tenant, ok := a.resolve(r.Header.Get("Authorization"))
		if !ok {
			a.logRejected("http", r.Header.Get("Authorization"))
			if a.recordFailure(sourceOf(r.RemoteAddr)) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "too many refused pushes: slow down", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="ego-ingest"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(withIngestSender(r.Context(), sender, tenant)))
	})
}

// interceptor is the OTLP/gRPC counterpart of middleware.
func (a *ingestAuth) interceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		var header string
		if v := md.Get("authorization"); len(v) > 0 {
			header = v[0]
		}
		sender, tenant, ok := a.resolve(header)
		if !ok {
			a.logRejected("grpc", header)
			if p, ok := peer.FromContext(ctx); ok && a.recordFailure(sourceOf(p.Addr.String())) {
				return nil, status.Error(codes.ResourceExhausted, "too many refused pushes: slow down")
			}
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}
		return handler(withIngestSender(ctx, sender, tenant), req)
	}
}

// normalizeIngestToken accepts the shared token as a bare value or as the header value "Bearer <token>", which is
// how it is usually kept when the same Secret feeds the senders' exporter headers.
func normalizeIngestToken(s string) string {
	s = strings.TrimSpace(s)
	if t, ok := bearerToken(s); ok {
		return t
	}
	return s
}
