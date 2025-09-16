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

		reEncodedErr := grpcerrors.ToGRPC(context.TODO(), decodedErr)

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
	reEncoded := grpcerrors.ToGRPC(context.TODO(), joined)
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

func TestToGRPCMessage(t *testing.T) {
	t.Parallel()
	t.Run("avoid prepending grpc status code", func(t *testing.T) {
		t.Parallel()
		err := errors.New("something")
		decoded := grpcerrors.FromGRPC(grpcerrors.ToGRPC(context.TODO(), err))
		assert.Equal(t, err.Error(), decoded.Error())
	})
	t.Run("keep extra context", func(t *testing.T) {
		t.Parallel()
		err := errors.New("something")
		wrapped := errors.Wrap(grpcerrors.ToGRPC(context.TODO(), err), "extra context")

		anotherErr := errors.New("another error")
		joined := stderrors.Join(wrapped, anotherErr)

		// Check that wrapped.Error() starts with "extra context"
		encoded := grpcerrors.ToGRPC(context.TODO(), joined)
		decoded := grpcerrors.FromGRPC(encoded)

		assert.ErrorContains(t, decoded, wrapped.Error()) //nolint:testifylint // don't use require since its not critical
		assert.ErrorContains(t, decoded, anotherErr.Error())
	})
}
