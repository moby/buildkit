package grpcerrors_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
		// Check that wrapped.Error() starts with "extra context"
		assert.True(t, strings.HasPrefix(wrapped.Error(), "extra context"), "expected wrapped error to start with 'extra context'")
		encoded := grpcerrors.ToGRPC(context.TODO(), wrapped)
		decoded := grpcerrors.FromGRPC(encoded)
		assert.Equal(t, wrapped.Error(), decoded.Error())
	})
}
