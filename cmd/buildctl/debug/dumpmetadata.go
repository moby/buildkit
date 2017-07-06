package debug

import (
	"fmt"
	"path/filepath"

	"github.com/urfave/cli"
)

var DumpMetadataCommand = cli.Command{
	Name:  "dump-metadata",
	Usage: "dump the meta in human-readable format.  This command requires the daemon NOT to be running.",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "root",
			Usage: "path to state directory",
			Value: ".buildstate",
		},
	},
	Action: func(clicontext *cli.Context) error {
		dbFile := filepath.Join(clicontext.String("root"), "metadata.db")
		return dumpBolt(dbFile, func(k, v []byte) string {
			return fmt.Sprintf("%q: %s", string(k), string(v))
		})
	},
}
