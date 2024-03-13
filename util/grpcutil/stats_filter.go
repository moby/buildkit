package grpcutil

import (
	"context"

	"google.golang.org/grpc/stats"
)

type contextKey int

const filterContextKey contextKey = iota

type StatsFilterFunc func(info *stats.RPCTagInfo) bool

func StatsFilter(h stats.Handler, fn StatsFilterFunc) stats.Handler {
	return &statsFilter{
		inner:  h,
		filter: fn,
	}
}

type statsFilter struct {
	inner  stats.Handler
	filter func(info *stats.RPCTagInfo) bool
}

func (s *statsFilter) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	if s.filter(info) {
		return context.WithValue(ctx, filterContextKey, struct{}{})
	}
	return s.inner.TagRPC(ctx, info)
}

func (s *statsFilter) HandleRPC(ctx context.Context, rpcStats stats.RPCStats) {
	if ctx.Value(filterContextKey) != nil {
		return
	}
	s.inner.HandleRPC(ctx, rpcStats)
}

func (s *statsFilter) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	return s.inner.TagConn(ctx, info)
}

func (s *statsFilter) HandleConn(ctx context.Context, connStats stats.ConnStats) {
	s.inner.HandleConn(ctx, connStats)
}
