package session

import (
	"context"
	"net"
	"net/url"
	"testing"

	"github.com/moby/buildkit/session/testutil"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestSessionSharedKeyMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sharedKey string
		encoded   bool
	}{
		{
			name:      "ascii",
			sharedKey: "context:%2B+plain",
		},
		{
			name:      "non-ascii",
			sharedKey: "context:\u65e9:%2B+plain",
			encoded:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := NewSession(t.Context(), tt.sharedKey)
			require.NoError(t, err)

			errDial := errors.New("stop after metadata capture")
			var called bool
			var gotProto string
			var gotMeta map[string][]string
			dialer := func(_ context.Context, proto string, meta map[string][]string) (net.Conn, error) {
				called = true
				gotProto = proto
				gotMeta = meta
				return nil, errDial
			}

			err = s.Run(t.Context(), dialer)
			require.ErrorIs(t, err, errDial)
			require.True(t, called)
			require.Equal(t, "h2c", gotProto)
			require.Equal(t, []string{s.ID()}, gotMeta[headerSessionID])
			if tt.encoded {
				require.Equal(t, []string{url.QueryEscape(tt.sharedKey)}, gotMeta[headerSessionSharedKey])
				require.Equal(t, []string{"1"}, gotMeta[headerSessionSharedKeyEncoded])
			} else {
				require.Equal(t, []string{tt.sharedKey}, gotMeta[headerSessionSharedKey])
				require.NotContains(t, gotMeta, headerSessionSharedKeyEncoded)
			}
		})
	}
}

func TestSessionSharedKeyRoundTrip(t *testing.T) {
	t.Parallel()

	sharedKey := "context:\u65e9:%2B+plain"
	s, err := NewSession(t.Context(), sharedKey)
	require.NoError(t, err)

	m, err := NewManager()
	require.NoError(t, err)

	dialer := Dialer(testutil.TestStream(testutil.Handler(m.HandleConn)))

	g, ctx := errgroup.WithContext(t.Context())
	g.Go(func() error {
		return s.Run(ctx, dialer)
	})
	g.Go(func() error {
		c, err := m.Get(ctx, s.ID(), false)
		if err != nil {
			return err
		}
		if c.SharedKey() != sharedKey {
			return errors.Errorf("expected shared key %q, got %q", sharedKey, c.SharedKey())
		}
		return s.Close()
	})

	require.NoError(t, g.Wait())
}
