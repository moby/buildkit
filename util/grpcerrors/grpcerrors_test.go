package grpcerrors_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/golang/protobuf/proto" //nolint:staticcheck
	"github.com/moby/buildkit/errdefs"
	solver "github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/stack"
	digest "github.com/opencontainers/go-digest"
	pkgerrors "github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestRoundTripGRPC_StandardErrors(t *testing.T) {
	err := fmt.Errorf("more context: %w: %w", fmt.Errorf("my error: %w", io.EOF), io.ErrShortWrite) //nolint:forbidigo
	require.EqualError(t, err, "more context: my error: EOF: short write")

	err = grpcerrors.FromGRPC(grpcerrors.ToGRPC(context.TODO(), err))
	require.EqualError(t, err, "more context: my error: EOF: short write")

	// First level uses Unwrap() []error.
	wrapper, ok := err.(interface{ Unwrap() []error })
	require.True(t, ok)
	errs := wrapper.Unwrap()
	require.Len(t, errs, 2)

	// First error has another wrapped error.
	err = errs[0]
	require.EqualError(t, err, "my error: EOF")

	err = errors.Unwrap(err)
	require.EqualError(t, err, "EOF")

	err = errors.Unwrap(err)
	require.NoError(t, err)

	// Second error has no wrapped error.
	err = errs[1]
	require.EqualError(t, err, "short write")

	err = errors.Unwrap(err)
	require.NoError(t, err)
}

func TestRoundTripGRPC_TypedError(t *testing.T) {
	dgst := digest.Digest("abc")
	err := solver.WrapVertex(io.EOF, dgst)
	require.EqualError(t, err, "EOF")

	err = grpcerrors.FromGRPC(grpcerrors.ToGRPC(context.TODO(), err))
	require.EqualError(t, err, "EOF")

	// Should be able to retrieve the VertexError and it should
	// be the top-most level.
	vtx, ok := err.(*solver.VertexError)
	require.True(t, ok)
	require.Equal(t, dgst, digest.Digest(vtx.Digest))

	// Wrapped error should be EOF.
	err = errors.Unwrap(err)
	require.EqualError(t, err, "EOF")

	// No more wrapped errors.
	err = errors.Unwrap(err)
	require.NoError(t, err)

	// Now test a nested typed error.
	err = fmt.Errorf("my error: %w", solver.WrapVertex(io.EOF, dgst)) //nolint:forbidigo
	require.EqualError(t, err, "my error: EOF")

	err = grpcerrors.FromGRPC(grpcerrors.ToGRPC(context.TODO(), err))
	require.EqualError(t, err, "my error: EOF")

	err = errors.Unwrap(err)
	require.Error(t, err)

	vtx, ok = err.(*solver.VertexError)
	require.True(t, ok)
	require.Equal(t, dgst, digest.Digest(vtx.Digest))

	// Wrapped error should be EOF.
	err = errors.Unwrap(err)
	require.EqualError(t, err, "EOF")

	// No more wrapped errors.
	err = errors.Unwrap(err)
	require.NoError(t, err)
}

func TestRoundTripGRPC_StackTraces(t *testing.T) {
	err := pkgerrors.New("my error")
	require.EqualError(t, err, "my error")

	err = grpcerrors.FromGRPC(grpcerrors.ToGRPC(context.TODO(), err))

	// Returned error should have a stack trace.
	require.Len(t, stack.Traces(err), 1)

	// Primitive error from pkgerrors is not wrapped.
	err = errors.Unwrap(err)
	require.NoError(t, err)

	// Wrap another error.
	err = pkgerrors.Wrap(io.EOF, "my error")
	require.EqualError(t, err, "my error: EOF")

	err = grpcerrors.FromGRPC(grpcerrors.ToGRPC(context.TODO(), err))

	// Should have a stack trace.
	require.Len(t, stack.Traces(err), 1)

	// Should be wrapped. Due to how pkgerrors is implemented,
	// a wrapped error is actually two wrapped errors.
	err = errors.Unwrap(err)
	require.EqualError(t, err, "my error: EOF")

	err = errors.Unwrap(err)
	require.EqualError(t, err, "EOF")

	// Multiple wrapped errors.
	err = pkgerrors.Wrap(pkgerrors.New("my error"), "with more context")
	require.EqualError(t, err, "with more context: my error")

	err = grpcerrors.FromGRPC(grpcerrors.ToGRPC(context.TODO(), err))

	// Has two stack traces.
	require.Len(t, stack.Traces(err), 2)

	// Wrapped with a single stack trace.
	err = errors.Unwrap(err)
	require.EqualError(t, err, "with more context: my error")
	require.Len(t, stack.Traces(err), 1)

	// Unwrap the message.
	err = errors.Unwrap(err)
	require.EqualError(t, err, "my error")
	require.Len(t, stack.Traces(err), 1)

	// No more wrapping.
	err = errors.Unwrap(err)
	require.NoError(t, err)
}

// TestRoundTripGRPC_MarshalV1_Compatibility checks that
// errors serialized with the V1 version of this code
// are handled correctly when they are unmarshaled.
func TestRoundTripGRPC_MarshalV1_Compatibility(t *testing.T) {
	// The v1 version of marshal had limited support
	// for structured errors. In particular, it only had
	// structured errors when TypedErrorProto and when stacks
	// were used so that's all we're going to test.
	//
	// We marshal all errors with marshalV1 and unmarshal it with
	// the new code to ensure it identifies the previous version
	// correctly and unmarshals it correctly.
	err := grpcerrors.FromGRPC(marshalV1(pkgerrors.New("my error")))

	require.EqualError(t, err, "my error")
	// The unmarshalV1 always adds an extra local stack
	// trace even if it didn't previously exist.
	require.Len(t, stack.Traces(err), 2)

	// Serialize a typed error and ensure everything works
	// correctly.
	dgst := digest.Digest("abc")
	err = solver.WrapVertex(pkgerrors.New("my error"), dgst)
	err = grpcerrors.FromGRPC(marshalV1(err))

	require.EqualError(t, err, "my error")
	// See this same call above for why this is 2 instead of 1.
	require.Len(t, stack.Traces(err), 2)

	// The vertex isn't structured in the same way as the original
	// wrapper, but As should work.
	var vtx *solver.VertexError
	require.ErrorAs(t, err, &vtx, "unable to find vertex error")
	require.Equal(t, dgst, digest.Digest(vtx.Digest))
}

// TestRoundTripGRPC_UnmarshalV1_Compatibility verifies that
// an error marshaled with the newer version of this library
// can be unmarshaled by the older version.
func TestRoundTripGRPC_UnmarshalV1_Compatibility(t *testing.T) {
	// Similar to the MarshalV1 test, the UnmarshalV1 has
	// limited support for structured errors so we're mostly
	// testing the same things.
	err := grpcerrors.UnmarshalV1(grpcerrors.ToGRPC(context.TODO(), pkgerrors.New("my error")))

	require.EqualError(t, err, "my error")
	// The unmarshalV1 always adds an extra local stack
	// trace even if it didn't previously exist.
	require.Len(t, stack.Traces(err), 2)

	// Serialize a typed error and ensure everything works
	// correctly.
	dgst := digest.Digest("abc")
	err = solver.WrapVertex(pkgerrors.New("my error"), dgst)
	err = grpcerrors.UnmarshalV1(grpcerrors.ToGRPC(context.TODO(), err))

	require.EqualError(t, err, "my error")
	// See this same call above for why this is 2 instead of 1.
	require.Len(t, stack.Traces(err), 2)

	// The vertex isn't structured in the same way as the original
	// wrapper, but As should work.
	var vtx *solver.VertexError
	require.ErrorAs(t, err, &vtx, "unable to find vertex error")
	require.Equal(t, dgst, digest.Digest(vtx.Digest))
}

func TestCode(t *testing.T) {
	for _, tt := range []struct {
		err error
		exp codes.Code
	}{
		{
			err: cerrdefs.ErrInvalidArgument,
			exp: codes.InvalidArgument,
		},
		{
			err: cerrdefs.ErrNotFound,
			exp: codes.NotFound,
		},
		{
			err: cerrdefs.ErrAlreadyExists,
			exp: codes.AlreadyExists,
		},
		{
			err: cerrdefs.ErrFailedPrecondition,
			exp: codes.FailedPrecondition,
		},
		{
			err: cerrdefs.ErrUnavailable,
			exp: codes.Unavailable,
		},
		{
			err: cerrdefs.ErrNotImplemented,
			exp: codes.Unimplemented,
		},
		{
			err: context.Canceled,
			exp: codes.Canceled,
		},
		{
			err: context.DeadlineExceeded,
			exp: codes.DeadlineExceeded,
		},
		{
			err: errdefs.Internal(io.EOF),
			exp: codes.Internal,
		},
		{
			err: io.EOF,
			exp: codes.Unknown,
		},
	} {
		t.Run(tt.err.Error(), func(t *testing.T) {
			got := grpcerrors.Code(tt.err)
			require.Equal(t, tt.exp, got)
		})
	}
}

func marshalV1(err error) error {
	if err == nil {
		return nil
	}
	st, ok := grpcerrors.AsGRPCStatus(err)
	if !ok || st == nil {
		st = status.New(grpcerrors.Code(err), err.Error())
	}
	if st.Code() != grpcerrors.Code(err) {
		code := grpcerrors.Code(err)
		if code == codes.OK {
			code = codes.Unknown
		}
		pb := st.Proto()
		pb.Code = int32(code)
		st = status.FromProto(pb)
	}

	// If the original error was wrapped with more context than the GRPCStatus error,
	// copy the original message to the GRPCStatus error
	if err.Error() != st.Message() {
		pb := st.Proto()
		pb.Message = err.Error()
		st = status.FromProto(pb)
	}

	var details []proto.Message

	for _, st := range stack.Traces(err) {
		details = append(details, st)
	}

	each(err, func(err error) {
		if te, ok := err.(grpcerrors.TypedError); ok {
			details = append(details, te.ToProto())
		}
	})

	if len(details) > 0 {
		if st2, err := withDetailsV1(st, details...); err == nil {
			st = st2
		}
	}

	return st.Err()
}

func each(err error, fn func(error)) {
	fn(err)
	if wrapped, ok := err.(interface {
		Unwrap() error
	}); ok {
		each(wrapped.Unwrap(), fn)
	}
}

func withDetailsV1(s *status.Status, details ...proto.Message) (*status.Status, error) {
	if s.Code() == codes.OK {
		return nil, errors.New("no error details for status with code OK")
	}
	p := s.Proto()
	for _, detail := range details {
		url, err := typeurl.TypeURL(detail)
		if err != nil {
			bklog.G(context.TODO()).Warnf("ignoring typed error %T: not registered", detail)
			continue
		}
		dt, err := json.Marshal(detail)
		if err != nil {
			return nil, err
		}
		p.Details = append(p.Details, &anypb.Any{TypeUrl: url, Value: dt})
	}
	return status.FromProto(p), nil
}
