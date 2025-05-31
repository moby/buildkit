package sshprovider_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/buildkit/cmd/buildctl/build"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/stretchr/testify/require"
)

func TestToAgentSource(t *testing.T) {
	configs, err := build.ParseSSH([]string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sshprovider.NewSSHAgentProvider(configs)
	ok := err == nil || strings.Contains(err.Error(), "invalid empty ssh agent socket")
	if !ok {
		t.Fatal(err)
	}

	_, err = build.ParseSSH([]string{"default=raw=true"})
	require.ErrorContains(t, err, "raw mode must supply exactly one socket path")

	dir := t.TempDir()
	normalFilePath := filepath.Join(dir, "not-a-socket")
	f, err := os.Create(normalFilePath)
	require.NoError(t, err)
	f.Close()

	configs, err = build.ParseSSH([]string{"default=raw=true," + normalFilePath})
	require.NoError(t, err)

	_, err = sshprovider.NewSSHAgentProvider(configs)
	require.ErrorContains(t, err, "raw mode only supported with socket paths")

	sockPath := filepath.Join(dir, "test.sock")
	l, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer l.Close()

	configs, err = build.ParseSSH([]string{"default=raw=true," + sockPath})
	require.NoError(t, err)

	_, err = sshprovider.NewSSHAgentProvider(configs)
	require.NoError(t, err)

	configs, err = build.ParseSSH([]string{"default=" + sockPath + ",raw=true"})
	require.NoError(t, err)
	_, err = sshprovider.NewSSHAgentProvider(configs)
	require.NoError(t, err)
}
