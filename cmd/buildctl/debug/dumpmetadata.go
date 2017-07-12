package debug

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/boltdb/bolt"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/urfave/cli"
)

var DumpMetadataCommand = cli.Command{
	Name:  "dump-metadata",
	Usage: "dump the meta in human-readable format.  This command requires the daemon NOT to be running.",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "root",
			Usage: "path to state directory",
			Value: appdefaults.Root,
		},
	},
	Action: func(clicontext *cli.Context) error {
		dbFile := filepath.Join(clicontext.String("root"), "metadata.db")
		return dumpBolt(dbFile, func(k, v []byte) string {
			return fmt.Sprintf("%q: %s", string(k), string(v))
		})
	},
}

func dumpBolt(dbFile string, stringifier func(k, v []byte) string) error {
	if dbFile == "" {
		return errors.New("dbfile not specified")
	}
	if dbFile == "-" {
		// user could still specify "/dev/stdin" but unlikely to work
		return errors.New("stdin unsupported")
	}
	db, err := bolt.Open(dbFile, 0400, &bolt.Options{ReadOnly: true, Timeout: 3 * time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	return db.View(func(tx *bolt.Tx) error {
		// TODO: JSON format?
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			return dumpBucket(name, b, "", stringifier)
		})
	})
}

func dumpBucket(name []byte, b *bolt.Bucket, indent string, stringifier func(k, v []byte) string) error {
	fmt.Printf("%sbucket %q:\n", indent, string(name))
	childrenIndent := indent + "  "
	return b.ForEach(func(k, v []byte) error {
		if bb := b.Bucket(k); bb != nil {
			return dumpBucket(k, bb, childrenIndent, stringifier)
		}
		fmt.Printf("%s%s\n", childrenIndent, stringifier(k, v))
		return nil
	})
}
