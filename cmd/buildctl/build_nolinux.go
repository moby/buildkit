//go:build !linux
// +build !linux

package main

import (
	"context"
	"syscall"

	"github.com/containerd/console"
	"github.com/moby/buildkit/session/sessionio"
)

func watchSignal(ctx context.Context, con console.Console) (<-chan syscall.Signal, <-chan sessionio.WinSize) {
	resizeCh := make(chan sessionio.WinSize, 1)
	signalCh := make(chan syscall.Signal, 1)
	return signalCh, resizeCh
}
