package api

import (
	"context"
	"time"

	"github.com/triageagent-dev/ego/internal/otlp"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// traceServiceServer implements coltracepb.TraceServiceServer, the OTLP/gRPC
// counterpart to otlpTracesHandler (POST /v1/traces on otlpAddr). Same
// destination (store.Ingest), same conversion target shape (the OTLP/JSON
// dict ParseExportRequest expects) - only the wire format differs, so this
// is the whole receiver: grpc already hands Export a decoded
// *ExportTraceServiceRequest, so there's no protobuf-unmarshal step here the
// way otlpTracesHandler needs one for application/x-protobuf bodies.
type traceServiceServer struct {
	coltracepb.UnimplementedTraceServiceServer
	store *SessionStore
}

func (s *traceServiceServer) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	body, err := otlp.ExportRequestToMap(req)
	if err != nil {
		return nil, err
	}
	s.store.Ingest(body)
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// newOTLPGRPCServer builds the gRPC server for the OTLP trace receiver.
// Never gated by sessionSecret, matching otlpAddr's HTTP counterpart -
// trace ingestion is a service-to-service push, not a UI/API call a signed
// session cookie applies to.
func newOTLPGRPCServer(store *SessionStore) *grpc.Server {
	s := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxOTLPBody),
		grpc.MaxConcurrentStreams(64),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 30 * time.Second, PermitWithoutStream: false}),
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: 5 * time.Minute, Time: 2 * time.Minute, Timeout: 20 * time.Second}),
	)
	coltracepb.RegisterTraceServiceServer(s, &traceServiceServer{store: store})
	return s
}
