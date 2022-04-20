//go:build linux
// +build linux

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/containerd/console"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
)

func resizeConsole(ctx context.Context, p gwclient.ContainerProcess, con console.Console) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-ch:
				size, err := con.Size()
				if err != nil {
					continue
				}
				p.Resize(ctx, gwclient.WinSize{Cols: uint32(size.Width), Rows: uint32(size.Height)})
			}
		}
	}()
	ch <- syscall.SIGWINCH
}
