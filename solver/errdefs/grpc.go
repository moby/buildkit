package errdefs

import (
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ToGRPC(err error) error {
	if err == nil {
		return nil
	}
	st, ok := AsGRPCStatus(err)
	if !ok || st == nil {
		st = status.New(Code(err), err.Error())
	}
	if st.Code() != Code(err) {
		pb := st.Proto()
		pb.Code = int32(Code(err))
		st = status.FromProto(pb)
	}

	var details []proto.Message

	for _, st := range Traces(err) {
		details = append(details, st)
	}

	for _, st := range Sources(err) {
		details = append(details, st)
	}

	var ve *VertexError
	if errors.As(err, &ve) {
		details = append(details, &ve.Vertex)
	}

	if len(details) > 0 {
		if st2, err := st.WithDetails(details...); err == nil {
			st = st2
		}
	}

	return st.Err()
}

func Code(err error) codes.Code {
	if se, ok := err.(interface {
		Code() codes.Code
	}); ok {
		return se.Code()
	}

	wrapped, ok := err.(interface {
		Unwrap() error
	})
	if ok {
		return Code(wrapped.Unwrap())
	}

	return status.FromContextError(err).Code()
}

func WrapCode(err error, code codes.Code) error {
	return &withCode{error: err, code: code}
}

func AsGRPCStatus(err error) (*status.Status, bool) {
	if err == nil {
		return nil, true
	}
	if se, ok := err.(interface {
		GRPCStatus() *status.Status
	}); ok {
		return se.GRPCStatus(), true
	}

	wrapped, ok := err.(interface {
		Unwrap() error
	})
	if ok {
		return AsGRPCStatus(wrapped.Unwrap())
	}

	return nil, false
}

func FromGRPC(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}

	pb := st.Proto()

	n := &spb.Status{
		Code:    pb.Code,
		Message: pb.Message,
	}

	details := make([]interface{}, 0, len(pb.Details))

	// details that we don't understand are copied as proto
	for _, d := range pb.Details {
		detail := &ptypes.DynamicAny{}
		if err := ptypes.UnmarshalAny(d, detail); err != nil {
			n.Details = append(n.Details, d)
			continue
		}
		switch detail.Message.(type) {
		case *Stack, *Vertex, *Source:
			details = append(details, detail.Message)
		default:
			n.Details = append(n.Details, d)
		}
	}

	err = status.FromProto(n).Err()

	for _, d := range details {
		switch v := d.(type) {
		case *Stack:
			if v != nil {
				err = &withStack{stack: *v, error: err}
			}
		case *Vertex:
			err = WrapVertex(err, digest.Digest(v.Digest))
		case *Source:
			if v != nil {
				err = WithSource(err, *v)
			}
		}
	}

	if !hasLocalStackTrace(err) {
		err = errors.WithStack(err)
	}

	return err
}

type withCode struct {
	code codes.Code
	error
}

func (e *withCode) Unwrap() error {
	return e.error
}
