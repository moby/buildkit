package debug

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/moby/buildkit/cache/metadata"
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
			raw := fmt.Sprintf("%q: %q", string(k), string(v))
			if len(v) == 0 {
				return raw
			}
			var mv metadata.Value
			if err := json.Unmarshal(v, &mv); err != nil {
				return raw
			}
			return fmt.Sprintf("%q: {\"Data\":%q, \"Index\":%q}", string(k), string(mv.Data), mv.Index)
		})
	},
}
