package retryhandler

import (
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetryConnectionReset(t *testing.T) {
	err := &url.Error{Op: http.MethodGet, URL: "https://registry.example/token", Err: &net.OpError{
		Op: "read", Net: "tcp", Err: errConnectionReset,
	}}
	require.True(t, retryError(err))
}
