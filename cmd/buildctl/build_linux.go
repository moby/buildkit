package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/containerd/console"
	"github.com/moby/buildkit/session/sessionio"
)

func watchSignal(ctx context.Context, con console.Console) (<-chan syscall.Signal, <-chan sessionio.WinSize) {
	resizeCh := make(chan sessionio.WinSize, 1)
	signalCh := make(chan syscall.Signal, 1)
	ch := make(chan os.Signal, 1)
	signals := []os.Signal{syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM}
	signal.Ignore(signals...)
	signal.Notify(ch, signals...)
	go func() {
		defer signal.Ignore(signals...)
		for {
			select {
			case ss := <-ch:
				switch ss {
				case syscall.SIGWINCH:
					size, err := con.Size()
					if err != nil {
						continue
					}
					select {
					case resizeCh <- sessionio.WinSize{Cols: uint32(size.Width), Rows: uint32(size.Height)}:
					default:
					}
				default:
					select {
					case signalCh <- ss.(syscall.Signal):
					default:
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	ch <- syscall.SIGWINCH
	return signalCh, resizeCh
}
