package grpcerrors

import (
	"google.golang.org/grpc/status"
)

// UnmarshalV1 exposes the internal unmarshalV1 function to
// the tests so it can be used without exposing the function
// as part of the public interface.
func UnmarshalV1(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}

	pb := st.Proto()
	return unmarshalV1(pb)
}
