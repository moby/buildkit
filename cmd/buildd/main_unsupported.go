// +build !standalone,!containerd standalone,containerd

package main

import (
	"errors"

	"github.com/moby/buildkit/control"
	"github.com/urfave/cli"
)

func appendFlags(f []cli.Flag) []cli.Flag {
	return f
}

func newController(c *cli.Context, root string) (*control.Controller, error) {
	return nil, errors.New("invalid build")
}
