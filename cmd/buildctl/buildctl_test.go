package main

import (
	"testing"

	"github.com/moby/buildkit/util/testutil/integration"
)

var testCases = map[string]integration.Test{
	"TestDiskUsage": testDiskUsage,
}

func TestCLIIntegration(t *testing.T) {
	integration.Run(t, testCases)
}
