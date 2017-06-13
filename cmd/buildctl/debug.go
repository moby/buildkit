package main

import (
	"github.com/tonistiigi/buildkit_poc/cmd/buildctl/debug"
	"github.com/urfave/cli"
)

var debugCommand = cli.Command{
	Name:  "debug",
	Usage: "debug utilities",
	Subcommands: []cli.Command{
		debug.DumpCommand,
	},
}
