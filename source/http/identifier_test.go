package http

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPIdentifierString(t *testing.T) {
	id, err := NewHTTPIdentifier("example.com/file.tar.gz", false)
	require.NoError(t, err)
	require.Equal(t, "http://example.com/file.tar.gz", id.String())
}

func TestHTTPSIdentifierString(t *testing.T) {
	id, err := NewHTTPIdentifier("example.com/file.tar.gz", true)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/file.tar.gz", id.String())
}
