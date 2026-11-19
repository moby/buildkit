package grpcerrors

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/moby/buildkit/util/stack"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func UnaryServerInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	resp, err = handler(ctx, req)
	oldErr := err
	if err != nil {
		stack.Helper()
		err = ToGRPC(ctx, err)
	}
	if oldErr != nil && err == nil {
		logErr := errors.Wrap(err, "invalid grpc error conversion")
		if os.Getenv("BUILDKIT_DEBUG_PANIC_ON_ERROR") == "1" {
			panic(logErr)
		}
		log.Printf("%v", logErr)
		err = oldErr
	}

	return resp, err
}

func StreamServerInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	err := ToGRPC(ss.Context(), handler(srv, ss))
	if err != nil {
		stack.Helper()
	}
	return err
}

func UnaryClientInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	err := invoker(ctx, method, req, reply, cc, opts...)
	if err != nil {
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.DeadlineExceeded && st.Message() == context.DeadlineExceeded.Error() && len(st.Details()) == 0 {
			// The server's deadline can fire before the local context timer.
			if deadline, hasDeadline := ctx.Deadline(); hasDeadline && !time.Now().Before(deadline) {
				<-ctx.Done()
			}
		}
		ctxErr := ctx.Err() //nolint:forbidigo // Match gRPC's status before restoring a custom cancellation cause.
		if ctxErr != nil {
			if ok && st.Code() == Code(ctxErr) && st.Message() == ctxErr.Error() && len(st.Details()) == 0 {
				return errors.WithStack(&contextCauseError{error: context.Cause(ctx), st: st})
			}
		}
	}
	err = FromGRPC(err)
	if err != nil {
		stack.Helper()
	}
	return err
}

func StreamClientInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	s, err := streamer(ctx, desc, cc, method, opts...)
	if err != nil {
		stack.Helper()
	}
	return s, ToGRPC(ctx, err)
}

// contextCauseError retains the RPC status independently of the cause's code.
type contextCauseError struct {
	error
	st *status.Status
}

func (e *contextCauseError) Unwrap() error {
	return e.error
}

func (e *contextCauseError) Code() codes.Code {
	return e.st.Code()
}

func (e *contextCauseError) GRPCStatus() *status.Status {
	pb := e.st.Proto()
	pb.Message = e.Error()
	return status.FromProto(pb)
}
