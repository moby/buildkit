// +build standalone

package main

import (
	"github.com/tonistiigi/buildkit_poc/control"
	"github.com/urfave/cli"
)

func appendFlags(f []cli.Flag) []cli.Flag {
	return f
}

func newController(c *cli.Context) (*control.Controller, error) {
	root := c.GlobalString("root")

	return control.NewStandalone(root)
}
