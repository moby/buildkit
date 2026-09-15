package gateway

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/client/buildid"
	"github.com/moby/buildkit/frontend/gateway"
	"google.golang.org/grpc/metadata"
)

func TestLookupRegistration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register bool
		deadline time.Duration
		elapsed  time.Duration
	}{
		{name: "late solve", register: true, elapsed: 4 * time.Second},
		{name: "missing solve", elapsed: 15 * time.Second},
		{name: "shorter caller deadline", deadline: time.Second, elapsed: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gw := NewGatewayForwarder()
				outgoing := buildid.AppendToOutgoingContext(t.Context(), "build")
				md, _ := metadata.FromOutgoingContext(outgoing)
				ctx := metadata.NewIncomingContext(t.Context(), md)
				if tc.deadline != 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.deadline)
					defer cancel()
				}
				bridge := &struct{ gateway.LLBBridgeForwarder }{}
				if tc.register {
					go func() {
						time.Sleep(4 * time.Second)
						gw.RegisterBuild(ctx, "build", bridge)
					}()
				}
				start := time.Now()
				got, err := gw.lookupForwarder(ctx)
				if tc.register {
					if err != nil || got != bridge {
						t.Errorf("late registration: bridge=%v err=%v", got, err)
					}
				} else if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("missing registration: expected deadline, got %v", err)
				}
				if elapsed := time.Since(start); elapsed != tc.elapsed {
					t.Errorf("lookup waited %s, want %s", elapsed, tc.elapsed)
				}
			})
		})
	}
}
