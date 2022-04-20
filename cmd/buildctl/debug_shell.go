package main

import (
	"context"
	"fmt"
	"os"

	"github.com/containerd/console"
	"github.com/moby/buildkit/client"
	bccommon "github.com/moby/buildkit/cmd/buildctl/common"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/urfave/cli"
)

var debugShellCommand = cli.Command{
	Name:   "shell",
	Usage:  "exec shell in a vertex",
	Action: debugShellAction,
}

var debugCloseCommand = cli.Command{
	Name:   "close",
	Usage:  "close a build job remaining for debugging",
	Action: debugCloseAction,
}

func debugCloseAction(clicontext *cli.Context) error {
	ref := clicontext.Args().Get(0)
	if ref == "" {
		return fmt.Errorf("ref must be specified")
	}
	c, err := bccommon.ResolveClient(clicontext)
	if err != nil {
		return err
	}
	ctx := clicontext.App.Metadata["context"].(context.Context)
	return c.DebugClose(ctx, ref)
}

func debugShellAction(clicontext *cli.Context) error {
	ref := clicontext.Args().Get(0)
	vtx := clicontext.Args().Get(1)
	if ref == "" || vtx == "" {
		return fmt.Errorf("ref and vertex must be specified")
	}
	c, err := bccommon.ResolveClient(clicontext)
	if err != nil {
		return err
	}
	ctx := clicontext.App.Metadata["context"].(context.Context)
	return execProcess(ctx, c, ref, vtx, gwclient.StartRequest{
		Args:   []string{"/bin/sh"},
		Stdin:  os.Stdin,
		Stdout: os.Stderr,
		Stderr: os.Stderr,
		Tty:    true,
	})
}

func execProcess(ctx context.Context, c *client.Client, ref, vtx string, req gwclient.StartRequest) error {
	con := console.Current()
	defer con.Reset()
	if err := con.SetRaw(); err != nil {
		return err
	}
	p, err := c.DebugExecProcess(ctx, ref, vtx, req)
	if err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}
	resizeConsole(ctx, p, con)
	return p.Wait()
}
