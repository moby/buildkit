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

var DumpCommand = cli.Command{
	Name:   "dump",
	Usage:  "dump LLB in human-readable format. LLB must be passed via stdin. This command does not require the daemon to be running.",
	Action: dumpLLB,
}

func dumpLLB(clicontext *cli.Context) error {
	ops, err := loadLLB(os.Stdin)
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
