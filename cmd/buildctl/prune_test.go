package main

import (
	"testing"

	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

func testPrune(t *testing.T, sb integration.Sandbox) {
	cmd := sb.Cmd("prune")
	err := cmd.Run()
	require.NoError(t, err)
}
