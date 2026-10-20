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

// The OTLP receivers are service-to-service pushes, so they cannot use the UI's cookie. With an ingest token
// set, a sender must present it as a bearer token (the OTLP exporters' `headers` setting); without one the
// receivers stay open, which is only safe when the network limits who can reach them.

func bearerMatches(header, token string) bool {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(header[len(prefix):])), []byte(token)) == 1
}

// requireIngestToken wraps an OTLP/HTTP handler; an empty token disables the check.
func requireIngestToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !bearerMatches(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ego-ingest"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ingestTokenInterceptor is the OTLP/gRPC counterpart of requireIngestToken.
func ingestTokenInterceptor(token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		for _, v := range md.Get("authorization") {
			if bearerMatches(v, token) {
				return handler(ctx, req)
			}
		}
		return nil, status.Error(codes.Unauthenticated, "unauthorized")
	}
}
