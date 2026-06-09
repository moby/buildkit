package main

import (
	"github.com/moby/buildkit/cmd/buildctl/debug"
	"github.com/urfave/cli/v3"
)

var debugCommand = &cli.Command{
	Name:  "debug",
	Usage: "debug utilities",
	Commands: []*cli.Command{
		debug.DumpLLBCommand,
		debug.DumpMetadataCommand,
		debug.WorkersCommand,
		debug.InfoCommand,
		debug.MonitorCommand,
		debug.LogsCommand,
		debug.CtlCommand,
		debug.GetCommand,
		debug.HistoriesCommand,
	},
}
