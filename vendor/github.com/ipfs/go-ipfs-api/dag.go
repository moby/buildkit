package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ipfs/go-ipfs-api/options"
	files "github.com/ipfs/go-ipfs-files"
)

func (s *Shell) DagGet(ref string, out interface{}) error {
	return s.Request("dag/get", ref).Exec(context.Background(), out)
}

func (s *Shell) DagPut(data interface{}, inputCodec, storeCodec string) (string, error) {
	return s.DagPutWithOpts(data, options.Dag.InputCodec(inputCodec), options.Dag.StoreCodec(storeCodec))
}

func (s *Shell) DagPutWithOpts(data interface{}, opts ...options.DagPutOption) (string, error) {
	cfg, err := options.DagPutOptions(opts...)
	if err != nil {
		return "", err
	}

	var r io.Reader
	switch data := data.(type) {
	case string:
		r = strings.NewReader(data)
	case []byte:
		r = bytes.NewReader(data)
	case io.Reader:
		r = data
	default:
		return "", fmt.Errorf("cannot current handle putting values of type %T", data)
	}

	fr := files.NewReaderFile(r)
	slf := files.NewSliceDirectory([]files.DirEntry{files.FileEntry("", fr)})
	fileReader := files.NewMultiFileReader(slf, true)

	var out struct {
		Cid struct {
			Target string `json:"/"`
		}
	}

	return out.Cid.Target, s.
		Request("dag/put").
		Option("input-codec", cfg.InputCodec).
		Option("store-codec", cfg.StoreCodec).
		Option("pin", cfg.Pin).
		Option("hash", cfg.Hash).
		Body(fileReader).
		Exec(context.Background(), &out)
}
