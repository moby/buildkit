package client

import (
	"context"
	"runtime"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/assert"
)

var testCases = map[string]integration.Test{
	"TestCallDiskUsage":   testCallDiskUsage,
	"TestBuildMultiMount": testBuildMultiMount,
}

func TestClientIntegration(t *testing.T) {
	integration.Run(t, testCases)
}

func testCallDiskUsage(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Address())
	assert.Nil(t, err)
	_, err = c.DiskUsage(context.TODO())
	assert.Nil(t, err)
}

func testBuildMultiMount(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	t.Parallel()
	c, err := New(sb.Address())
	assert.Nil(t, err)

	alpine := llb.Image("docker.io/library/alpine:latest")
	ls := alpine.Run(llb.Shlex("/bin/ls -l"))
	busybox := llb.Image("docker.io/library/busybox:latest")
	cp := ls.Run(llb.Shlex("/bin/cp -a /busybox/etc/passwd baz"))
	cp.AddMount("/busybox", busybox)

	def, err := cp.Marshal()
	assert.Nil(t, err)

	err = c.Solve(context.TODO(), def, SolveOpt{}, nil)
	assert.Nil(t, err)
}

func requiresLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}
}
