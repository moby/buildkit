// +build containerd

package main

import (
	"github.com/tonistiigi/buildkit_poc/control"
	"github.com/urfave/cli"
)

func appendFlags(f []cli.Flag) []cli.Flag {
	return append(f, []cli.Flag{
		cli.StringFlag{
			Name:  "containerd",
			Usage: "containerd socket",
			Value: "/run/containerd/containerd.sock",
		},
	}...)
}

func newController(c *cli.Context) (*control.Controller, error) {
	root := c.GlobalString("root")
	socket := c.GlobalString("containerd")

	return control.NewContainerd(root, socket)
}
