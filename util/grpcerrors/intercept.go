package grpcerrors

import (
	"context"
	"log"
	"os"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

func UnaryServerInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	resp, err = handler(ctx, req)
	oldErr := err
	if err != nil {
		err = ToGRPC(err)
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

func StreamServerInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return ToGRPC(handler(srv, ss))
}

func UnaryClientInterceptor(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return FromGRPC(invoker(ctx, method, req, reply, cc, opts...))
}

func StreamClientInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	s, err := streamer(ctx, desc, cc, method, opts...)
	return s, ToGRPC(err)
}
