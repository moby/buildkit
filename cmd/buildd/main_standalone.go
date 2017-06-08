// +build standalone

package main

import (
	"github.com/tonistiigi/buildkit_poc/control"
	"github.com/urfave/cli"
)

func newController(c *cli.Context) (*control.Controller, error) {
	root := c.GlobalString("root")

	return control.NewStandalone(root)
}
