// +build !standalone,!containerd

package main

import (
	"errors"

	"github.com/tonistiigi/buildkit_poc/control"
	"github.com/urfave/cli"
)

func appendFlags(f []cli.Flag) []cli.Flag {
	return f
}

func newController(c *cli.Context) (*control.Controller, error) {
	return nil, errors.New("invalid build")
}
