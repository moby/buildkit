//go:build !linux
// +build !linux

package main

import (
	"context"

	"github.com/containerd/console"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
)

func resizeConsole(ctx context.Context, p gwclient.ContainerProcess, con console.Console) {}
