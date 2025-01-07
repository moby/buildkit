package main

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/sqlcachestorage"
	"go.etcd.io/bbolt"
)

func main() {
	infile, outfile := os.Args[1], os.Args[2]

	out, err := sqlcachestorage.NewStore(outfile)
	if err != nil {
		panic(err)
	}
	defer out.Close()

	if err := out.AutoMigrate(); err != nil {
		panic(err)
	}

	in, err := bbolt.Open(infile, 0600, &bbolt.Options{
		ReadOnly: true,
	})
	if err != nil {
		panic(err)
	}
	defer in.Close()

	in.View(func(tx *bbolt.Tx) error {
		results := tx.Bucket([]byte("_result"))
		if results == nil {
			return nil
		}

		if err := results.ForEachBucket(func(id []byte) error {
			b := results.Bucket(id)
			if b == nil {
				return nil
			}

			return b.ForEach(func(k, v []byte) error {
				var res solver.CacheResult
				if err := json.Unmarshal(v, &res); err != nil {
					return err
				}
				return out.AddResult(string(id), res)
			})
		}); err != nil {
			return err
		}

		links := tx.Bucket([]byte("_links"))
		if links == nil {
			return nil
		}

		if err := links.ForEachBucket(func(id []byte) error {
			b := links.Bucket(id)
			if b == nil {
				return nil
			}

			return b.ForEach(func(k, _ []byte) error {
				index := bytes.LastIndexByte(k, '@')
				if index < 0 {
					return nil
				}
				target := k[index+1:]

				var res solver.CacheInfoLink
				if err := json.Unmarshal(k[:index], &res); err != nil {
					return err
				}
				return out.AddLink(string(id), res, string(target))
			})
		}); err != nil {
			return err
		}

		return nil
	})
}
