package local

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalIdentifierString(t *testing.T) {
	id, err := NewLocalIdentifier("context")
	require.NoError(t, err)
	require.Equal(t, "local://context", id.String())
}
