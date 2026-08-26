package contentutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderFromRefUsesContext(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(context.Canceled)

	ref := strings.TrimPrefix(srv.URL, "http://") + "/buildkit/test:latest"
	_, _, err := ProviderFromRef(ctx, ref)
	require.ErrorIs(t, err, context.Canceled)
}
