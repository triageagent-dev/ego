package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
// With neither configured the receivers stay open, which is only safe when the network limits who can reach them.
type ingestAuth struct {
	globalToken string
	senders     *SenderStore

	rejectMu   sync.Mutex
	rejectLast time.Time
	rejected   int
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

func (a *ingestAuth) required() bool {
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

// resolve returns the sender a request authenticates as ("" for the shared token or an open receiver) and
// whether it may send at all.
func (a *ingestAuth) resolve(header string) (sender string, ok bool) {
	if !a.required() {
		return "", true
	}
	got, has := bearerToken(header)
	if !has {
		return "", false
	}
	if a.globalToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(a.globalToken)) == 1 {
		return "", true
	}
	return a.senders.Resolve(got)
}

type ingestSenderKey struct{}

func withIngestSender(ctx context.Context, sender string) context.Context {
	return context.WithValue(ctx, ingestSenderKey{}, sender)
}

// ingestSender is the sender the receiver authenticated the sender as ("" when none).
func ingestSender(ctx context.Context) string {
	t, _ := ctx.Value(ingestSenderKey{}).(string)
	return t
}

// middleware wraps an OTLP/HTTP handler.
func (a *ingestAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sender, ok := a.resolve(r.Header.Get("Authorization"))
		if !ok {
			a.logRejected("http", r.Header.Get("Authorization"))
			w.Header().Set("WWW-Authenticate", `Bearer realm="ego-ingest"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(withIngestSender(r.Context(), sender)))
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
		sender, ok := a.resolve(header)
		if !ok {
			a.logRejected("grpc", header)
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}
		return handler(withIngestSender(ctx, sender), req)
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
