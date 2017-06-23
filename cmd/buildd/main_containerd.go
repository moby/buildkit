// +build containerd,!standalone

package main

import (
	"github.com/moby/buildkit/control"
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

// root must be an absolute path
func newController(c *cli.Context, root string) (*control.Controller, error) {
	socket := c.GlobalString("containerd")

	return control.NewContainerd(root, socket)
}
