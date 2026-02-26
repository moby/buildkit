package grpcerrors_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"

	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// helper types used by Code/AsGRPCStatus tests
type testCodeError struct{ c codes.Code }

func (e testCodeError) Error() string    { return e.c.String() }
func (e testCodeError) Code() codes.Code { return e.c }

type testGRPCStatusError struct{ st *status.Status }

func (e testGRPCStatusError) Error() string              { return e.st.Err().Error() }
func (e testGRPCStatusError) GRPCStatus() *status.Status { return e.st }

func TestFromGRPCPreserveUnknownTypes(t *testing.T) {
	t.Parallel()
	const (
		unknownType  = "type.googleapis.com/unknown.Type"
		unknownValue = "unknown value"

		errMessage = "something failed"
		errCode    = codes.Internal
	)
	raw := &anypb.Any{
		TypeUrl: unknownType,
		Value:   []byte(unknownValue),
	}

	pb := &spb.Status{
		Code:    int32(errCode),
		Message: errMessage,
		Details: []*anypb.Any{raw},
	}

	assertErrorProperties := func(t *testing.T, err error) {
		require.Error(t, err)
		assert.Equal(t, fmt.Sprintf("%s: %s", errCode, errMessage), err.Error())

		st, ok := status.FromError(err)
		require.True(t, ok)

		details := st.Proto().Details
		require.Len(t, details, 1)
		assert.Equal(t, unknownType, details[0].TypeUrl)
		assert.Equal(t, []byte(unknownValue), details[0].Value)
	}

	t.Run("encode", func(t *testing.T) {
		t.Parallel()
		err := grpcerrors.FromGRPC(status.FromProto(pb).Err())
		assertErrorProperties(t, err)
	})

	t.Run("roundtrip", func(t *testing.T) {
		t.Parallel()

		decodedErr := grpcerrors.FromGRPC(status.FromProto(pb).Err())

		reEncodedErr := grpcerrors.ToGRPC(t.Context(), decodedErr)

		reDecodedErr := grpcerrors.FromGRPC(reEncodedErr)
		assertErrorProperties(t, reDecodedErr)
	})
}

func TestFromGRPCPreserveTypes(t *testing.T) {
	t.Parallel()
	const (
		unknownType  = "type.googleapis.com/unknown.Type"
		unknownValue = "unknown value"

		errMessage = "something failed"
		errCode    = codes.Internal
	)
	raw := &anypb.Any{
		TypeUrl: unknownType,
		Value:   []byte(unknownValue),
	}

	pb := &spb.Status{
		Code:    int32(errCode),
		Message: errMessage,
		Details: []*anypb.Any{raw},
	}

	// Decode the original status into a Go error, then join it with a typed error
	decodedErr := grpcerrors.FromGRPC(status.FromProto(pb).Err())
	// Create a typed error (errdefs.Solve) and wrap a base error with it
	typed := &errdefs.Solve{InputIDs: []string{"typed-input"}}
	typedErr := typed.WrapError(stderrors.New("typed-base"))
	joined := stderrors.Join(decodedErr, typedErr)

	// Re-encode and decode the joined error and ensure the unknown detail is preserved
	reEncoded := grpcerrors.ToGRPC(t.Context(), joined)
	reDecoded := grpcerrors.FromGRPC(reEncoded)

	require.Error(t, reDecoded)

	// Ensure the typed error was preserved by finding the SolveError in the decoded error
	var se *errdefs.SolveError
	require.ErrorAs(t, reDecoded, &se)
	require.NotNil(t, se)
	assert.Equal(t, []string{"typed-input"}, se.InputIDs)

	// And ensure the original unknown detail is still present
	assert.ErrorContains(t, reDecoded, errMessage)
}

func TestCode(t *testing.T) {
	t.Parallel()

	t.Run("uses Code() method", func(t *testing.T) {
		c := grpcerrors.Code(testCodeError{c: codes.InvalidArgument})
		assert.Equal(t, codes.InvalidArgument, c)
	})

	t.Run("uses GRPCStatus() method", func(t *testing.T) {
		st := status.New(codes.PermissionDenied, "denied")
		c := grpcerrors.Code(testGRPCStatusError{st: st})
		assert.Equal(t, codes.PermissionDenied, c)
	})

	t.Run("unwraps single wrapped error", func(t *testing.T) {
		inner := testCodeError{c: codes.FailedPrecondition}
		outer := errors.Wrap(inner, "wrap")
		c := grpcerrors.Code(outer)
		assert.Equal(t, codes.FailedPrecondition, c)
	})

	t.Run("chooses first non-OK from joined errors", func(t *testing.T) {
		j := stderrors.Join(testCodeError{c: codes.OK}, testCodeError{c: codes.InvalidArgument}, testCodeError{c: codes.FailedPrecondition})
		c := grpcerrors.Code(j)
		assert.Equal(t, codes.InvalidArgument, c)
	})

	t.Run("maps context errors to codes", func(t *testing.T) {
		c := grpcerrors.Code(context.Canceled)
		assert.Equal(t, codes.Canceled, c)
		c = grpcerrors.Code(context.DeadlineExceeded)
		assert.Equal(t, codes.DeadlineExceeded, c)
	})
}

func TestAsGRPCStatus(t *testing.T) {
	t.Parallel()

	t.Run("nil returns ok with nil status", func(t *testing.T) {
		st, ok := grpcerrors.AsGRPCStatus(nil)
		require.True(t, ok)
		assert.Nil(t, st)
	})

	t.Run("direct GRPCStatus is returned", func(t *testing.T) {
		in := status.New(codes.NotFound, "nope")
		st, ok := grpcerrors.AsGRPCStatus(testGRPCStatusError{st: in})
		require.True(t, ok)
		assert.Equal(t, in.Code(), st.Code())
	})

	t.Run("finds status through a wrapped error", func(t *testing.T) {
		in := status.New(codes.Unavailable, "down")
		outer := errors.Wrap(testGRPCStatusError{st: in}, "wrap")
		st, ok := grpcerrors.AsGRPCStatus(outer)
		require.True(t, ok)
		assert.Equal(t, in.Code(), st.Code())
	})

	t.Run("finds status in joined errors", func(t *testing.T) {
		in := status.New(codes.ResourceExhausted, "res")
		j := stderrors.Join(testCodeError{c: codes.InvalidArgument}, testGRPCStatusError{st: in})
		st, ok := grpcerrors.AsGRPCStatus(j)
		require.True(t, ok)
		assert.Equal(t, in.Code(), st.Code())
	})

	t.Run("returns false when no status found", func(t *testing.T) {
		st, ok := grpcerrors.AsGRPCStatus(testCodeError{c: codes.InvalidArgument})
		assert.False(t, ok)
		assert.Nil(t, st)
	})
}

func TestToGRPCMessage(t *testing.T) {
	t.Parallel()
	t.Run("avoid prepending grpc status code", func(t *testing.T) {
		t.Parallel()
		err := errors.New("something")
		decoded := grpcerrors.FromGRPC(grpcerrors.ToGRPC(t.Context(), err))
		assert.Equal(t, err.Error(), decoded.Error())
	})
	t.Run("keep extra context", func(t *testing.T) {
		t.Parallel()
		err := errors.New("something")
		wrapped := errors.Wrap(grpcerrors.ToGRPC(t.Context(), err), "extra context")

		anotherErr := errors.New("another error")
		joined := stderrors.Join(wrapped, anotherErr)

		// Check that wrapped.Error() starts with "extra context"
		encoded := grpcerrors.ToGRPC(t.Context(), joined)
		decoded := grpcerrors.FromGRPC(encoded)

		assert.ErrorContains(t, decoded, wrapped.Error()) //nolint:testifylint // error is not critical
		assert.ErrorContains(t, decoded, anotherErr.Error())
	})
}
