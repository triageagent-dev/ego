package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The OTLP receivers are service-to-service pushes, so they cannot use the UI's cookie. A sender presents a
// bearer token (the OTLP exporters' `headers` setting). Two kinds are accepted:
//
//   - a tenant token, minted by an admin for one sender: everything the sender exports belongs to that tenant;
//   - the shared --otlp-token: a trusted sender whose spans keep whatever tenant attribute they carry.
//
// With neither configured the receivers stay open, which is only safe when the network limits who can reach them.
type ingestAuth struct {
	globalToken string
	tenants     *TenantStore
}

func (a *ingestAuth) required() bool {
	return a != nil && (a.globalToken != "" || a.tenants.HasTokens())
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

// resolve returns the tenant a request authenticates as ("" for the shared token or an open receiver) and
// whether it may send at all.
func (a *ingestAuth) resolve(header string) (tenant string, ok bool) {
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
	return a.tenants.Resolve(got)
}

type ingestTenantKey struct{}

func withIngestTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, ingestTenantKey{}, tenant)
}

// ingestTenant is the tenant the receiver authenticated the sender as ("" when none).
func ingestTenant(ctx context.Context) string {
	t, _ := ctx.Value(ingestTenantKey{}).(string)
	return t
}

// middleware wraps an OTLP/HTTP handler.
func (a *ingestAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := a.resolve(r.Header.Get("Authorization"))
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ego-ingest"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(withIngestTenant(r.Context(), tenant)))
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
		tenant, ok := a.resolve(header)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}
		return handler(withIngestTenant(ctx, tenant), req)
	}
}
