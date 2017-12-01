package main

import (
	"testing"

	"github.com/moby/buildkit/util/testutil/integration"
)

func TestCLIIntegration(t *testing.T) {
	integration.Run(t, []integration.Test{
		testDiskUsage,
		testBuildWithLocalFiles,
		testBuildLocalExporter,
		testBuildContainerdExporter,
	})
}
