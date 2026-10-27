package api

import (
	"context"
	"errors"
	"time"

	"github.com/triageagent-dev/ego/internal/otlp"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/gzip" // registers the decompressor; the OpenTelemetry Collector's gRPC exporter sends gzip by default
	protoname "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/status"
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
	if errors.Is(err, otlp.ErrBodyTooDense) {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	if err != nil {
		return nil, err
	}
	s.store.IngestBound(ingestSender(ctx), ingestTenant(ctx), body)
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// newOTLPGRPCServer builds the gRPC server for the OTLP trace receiver.
// Never gated by sessionSecret, matching otlpAddr's HTTP counterpart -
// trace ingestion is a service-to-service push, not a UI/API call a signed
// session cookie applies to. Senders must present a bearer credential when ingest tokens are configured (see ingestAuth).
func newOTLPGRPCServer(store *SessionStore, auth *ingestAuth) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxOTLPBody),
		grpc.MaxConcurrentStreams(64),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 30 * time.Second, PermitWithoutStream: false}),
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: 5 * time.Minute, Time: 2 * time.Minute, Timeout: 20 * time.Second}),
	}
	if auth != nil {
		opts = append(opts, grpc.InTapHandle(auth.tapHandle()))
	}
	opts = append(opts, grpc.ForceServerCodecV2(denseGuardCodec{encodingProto()}))
	s := grpc.NewServer(opts...)
	coltracepb.RegisterTraceServiceServer(s, &traceServiceServer{store: store})
	return s
}

// denseGuardCodec is grpc-go's protobuf codec, except that an export holding more nested messages than a trace can is
// refused before it is unmarshalled: unmarshalling is where a few megabytes of tiny messages become a gigabyte.
type denseGuardCodec struct{ encoding.CodecV2 }

func (c denseGuardCodec) Unmarshal(data mem.BufferSlice, v any) error {
	if _, ok := v.(*coltracepb.ExportTraceServiceRequest); ok {
		buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
		dense := otlp.ExportTooDense(buf.ReadOnlyData())
		buf.Free()
		if dense {
			return otlp.ErrBodyTooDense
		}
	}
	return c.CodecV2.Unmarshal(data, v)
}

// encodingProto is grpc-go's own protobuf codec, which the guard wraps.
func encodingProto() encoding.CodecV2 { return encoding.GetCodecV2(protoname.Name) }
