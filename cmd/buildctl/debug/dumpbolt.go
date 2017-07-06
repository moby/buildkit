package debug

import (
	"fmt"
	"time"

	"github.com/boltdb/bolt"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

var DumpBoltCommand = cli.Command{
	Name:  "dump-bolt",
	Usage: "dump an arbitrary bolt db file in human-readable format.  This command does not need the daemon to be running.",
	// unlike dumpLLB(), dumpBolt() does not support reading from stdin
	ArgsUsage: "<dbfile>",
	Action: func(clicontext *cli.Context) error {
		dbFile := clicontext.Args().First()
		return dumpBolt(dbFile, func(k, v []byte) string {
			return fmt.Sprintf("%q: %q", string(k), string(v))
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
