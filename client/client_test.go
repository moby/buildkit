package client

import (
	"context"
	"runtime"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testCases = map[string]integration.Test{
	"TestCallDiskUsage":   testCallDiskUsage,
	"TestBuildMultiMount": testBuildMultiMount,
}

func TestClientIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	for _, br := range integration.List() {
		for name, tc := range testCases {
			ok := t.Run(name+"/worker="+br.Name(), func(t *testing.T) {
				sb, close, err := br.New()
				if err != nil {
					if errors.Cause(err) == integration.ErrorRequirements {
						t.Skip(err.Error())
					}
					require.NoError(t, err)
				}
				defer func() {
					assert.NoError(t, close())
					if t.Failed() {
						sb.PrintLogs(t)
					}
				}()
				tc(t, sb)
			})
			require.True(t, ok)
		}
	}
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
