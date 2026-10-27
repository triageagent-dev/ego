package otlp

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// The cost of an export is in its messages: a 2 byte message (an empty attribute) becomes a struct of a few hundred
// bytes before any JSON exists, so 7 MiB of them took a gigabyte to decode. A real export has a nested message about
// every 12 bytes or more (an attribute is a key-value message holding a value message); this one is refused if it
// has more than one per 4 bytes, and never more than maxExportMessages however large it is.
const (
	minWireBytesPerMessage = 4
	maxExportMessages      = 1_200_000
)

// ExportTooDense reports whether raw, a protobuf ExportTraceServiceRequest, holds more nested messages than an
// export can. It walks the wire format against the message's schema and allocates nothing; bytes it cannot read are
// not its business, and proto.Unmarshal reports them.
func ExportTooDense(raw []byte) bool {
	budget := min(len(raw)/minWireBytesPerMessage+1000, maxExportMessages)
	return !withinMessages(raw, (&coltracepb.ExportTraceServiceRequest{}).ProtoReflect().Descriptor(), &budget)
}

// withinMessages counts the messages nested in b, a message of type md, against budget.
func withinMessages(b []byte, md protoreflect.MessageDescriptor, budget *int) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return true
		}
		b = b[n:]
		if typ != protowire.BytesType {
			if n = protowire.ConsumeFieldValue(num, typ, b); n < 0 {
				return true
			}
			b = b[n:]
			continue
		}
		v, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return true
		}
		b = b[n:]
		if fd := md.Fields().ByNumber(num); fd != nil && fd.Message() != nil {
			if *budget--; *budget < 0 || !withinMessages(v, fd.Message(), budget) {
				return false
			}
		}
	}
	return true
}
