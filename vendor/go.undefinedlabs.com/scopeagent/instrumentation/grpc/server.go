package grpc

import (
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/opentracing/opentracing-go/log"
	"go.undefinedlabs.com/scopeagent/instrumentation"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// OpenTracingServerInterceptor returns a grpc.UnaryServerInterceptor suitable
// for use in a grpc.NewServer call.
//
// For example:
//
//     s := grpc.NewServer(
//         ...,  // (existing ServerOptions)
//         grpc.UnaryInterceptor(otgrpc.OpenTracingServerInterceptor(tracer)))
//
// All gRPC server spans will look for an OpenTracing SpanContext in the gRPC
// metadata; if found, the server span will act as the ChildOf that RPC
// SpanContext.
//
// Root or not, the server Span will be embedded in the context.Context for the
// application-specific gRPC handler(s) to access.
func OpenTracingServerInterceptor(tracer opentracing.Tracer, optFuncs ...Option) grpc.UnaryServerInterceptor {
	otgrpcOpts := newOptions()
	otgrpcOpts.apply(optFuncs...)
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		if _, ok := tracer.(opentracing.NoopTracer); ok {
			tracer = instrumentation.Tracer()
		}
		spanContext, err := extractSpanContext(ctx, tracer)
		if err != nil && err != opentracing.ErrSpanContextNotFound {
			instrumentation.Logger().Println(err)
		}
		if otgrpcOpts.inclusionFunc != nil &&
			!otgrpcOpts.inclusionFunc(spanContext, info.FullMethod, req, nil) {
			return handler(ctx, req)
		}
		serverSpan := tracer.StartSpan(
			info.FullMethod,
			ext.RPCServerOption(spanContext),
			gRPCComponentTag,
			gRPCPeerServiceTag,
		)
		defer serverSpan.Finish()
		serverSpan.SetTag(MethodName, info.FullMethod)
		serverSpan.SetTag(MethodType, "UNITARY")

		ctx = opentracing.ContextWithSpan(ctx, serverSpan)
		if otgrpcOpts.logPayloads {
			serverSpan.LogFields(log.Object("gRPC request", req))
		}
		resp, err = handler(ctx, req)
		if err == nil {
			if otgrpcOpts.logPayloads {
				serverSpan.LogFields(log.Object("gRPC response", resp))
			}
			serverSpan.SetTag(Status, "OK")
		} else {
			SetSpanTags(serverSpan, err, false)
			serverSpan.LogFields(log.String("event", "error"), log.String("message", err.Error()))
		}
		if otgrpcOpts.decorator != nil {
			otgrpcOpts.decorator(serverSpan, info.FullMethod, req, resp, err)
		}
		return resp, err
	}
}

// OpenTracingStreamServerInterceptor returns a grpc.StreamServerInterceptor suitable
// for use in a grpc.NewServer call. The interceptor instruments streaming RPCs by
// creating a single span to correspond to the lifetime of the RPC's stream.
//
// For example:
//
//     s := grpc.NewServer(
//         ...,  // (existing ServerOptions)
//         grpc.StreamInterceptor(otgrpc.OpenTracingStreamServerInterceptor(tracer)))
//
// All gRPC server spans will look for an OpenTracing SpanContext in the gRPC
// metadata; if found, the server span will act as the ChildOf that RPC
// SpanContext.
//
// Root or not, the server Span will be embedded in the context.Context for the
// application-specific gRPC handler(s) to access.
func OpenTracingStreamServerInterceptor(tracer opentracing.Tracer, optFuncs ...Option) grpc.StreamServerInterceptor {
	otgrpcOpts := newOptions()
	otgrpcOpts.apply(optFuncs...)
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if _, ok := tracer.(opentracing.NoopTracer); ok {
			tracer = instrumentation.Tracer()
		}
		spanContext, err := extractSpanContext(ss.Context(), tracer)
		if err != nil && err != opentracing.ErrSpanContextNotFound {
			instrumentation.Logger().Println(err)
		}
		if otgrpcOpts.inclusionFunc != nil &&
			!otgrpcOpts.inclusionFunc(spanContext, info.FullMethod, nil, nil) {
			return handler(srv, ss)
		}

		serverSpan := tracer.StartSpan(
			info.FullMethod,
			ext.RPCServerOption(spanContext),
			gRPCComponentTag,
			gRPCPeerServiceTag,
		)
		defer serverSpan.Finish()
		serverSpan.SetTag(MethodName, info.FullMethod)
		if info.IsClientStream {
			serverSpan.SetTag(MethodType, "CLIENT_STREAMING")
		}
		if info.IsServerStream {
			serverSpan.SetTag(MethodType, "SERVER_STREAMING")
		}

		ss = &openTracingServerStream{
			ServerStream: ss,
			ctx:          opentracing.ContextWithSpan(ss.Context(), serverSpan),
		}
		err = handler(srv, ss)
		if err != nil {
			SetSpanTags(serverSpan, err, false)
			serverSpan.LogFields(log.String("event", "error"), log.String("message", err.Error()))
		}
		if otgrpcOpts.decorator != nil {
			otgrpcOpts.decorator(serverSpan, info.FullMethod, nil, nil, err)
		}
		return err
	}
}

type openTracingServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (ss *openTracingServerStream) Context() context.Context {
	return ss.ctx
}

func extractSpanContext(ctx context.Context, tracer opentracing.Tracer) (opentracing.SpanContext, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		md = metadata.New(nil)
	}
	return tracer.Extract(opentracing.HTTPHeaders, metadataReaderWriter{md})
}

// Get server interceptors
func GetServerInterceptors() []grpc.ServerOption {
	tracer := instrumentation.Tracer()
	return []grpc.ServerOption{
		grpc.UnaryInterceptor(OpenTracingServerInterceptor(tracer)),
		grpc.StreamInterceptor(OpenTracingStreamServerInterceptor(tracer)),
	}
}

func NewServer(opts ...grpc.ServerOption) *grpc.Server {
	opts = append(opts, GetServerInterceptors()...)
	return grpc.NewServer(opts...)
}
