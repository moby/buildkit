package debug

import (
	"encoding/json"
	"io"
	"os"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

var DumpLLBCommand = cli.Command{
	Name:      "dump-llb",
	Usage:     "dump LLB in human-readable format. LLB can be also passed via stdin. This command does not require the daemon to be running.",
	ArgsUsage: "<llbfile>",
	Action:    dumpLLB,
}

func dumpLLB(clicontext *cli.Context) error {
	var r io.Reader
	if llbFile := clicontext.Args().First(); llbFile != "" && llbFile != "-" {
		f, err := os.Open(llbFile)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	} else {
		r = os.Stdin
	}
	ops, err := loadLLB(r)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	for _, op := range ops {
		if err := enc.Encode(op); err != nil {
			return err
		}
	}
	return nil
}

type llbOp struct {
	Op     pb.Op
	Digest digest.Digest
}

func loadLLB(r io.Reader) ([]llbOp, error) {
	bs, err := llb.ReadFrom(r)
	if err != nil {
		return nil, err
	}
	var ops []llbOp
	for _, dt := range bs {
		var op pb.Op
		if err := (&op).Unmarshal(dt); err != nil {
			return nil, errors.Wrap(err, "failed to parse op")
		}
		dgst := digest.FromBytes(dt)
		ops = append(ops, llbOp{Op: op, Digest: dgst})
	}
	return ops, nil
}
