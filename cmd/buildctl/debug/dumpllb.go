package debug

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

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
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "dot",
			Usage: "Output dot format",
		},
	},
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
	if clicontext.Bool("dot") {
		writeDot(ops, os.Stdout)
	} else {
		enc := json.NewEncoder(os.Stdout)
		for _, op := range ops {
			if err := enc.Encode(op); err != nil {
				return err
			}
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

func writeDot(ops []llbOp, w io.Writer) {
	fmt.Fprintln(w, "digraph {")
	defer fmt.Fprintln(w, "}")
	for _, op := range ops {
		name, shape := attr(op.Digest, op.Op)
		fmt.Fprintf(w, "  \"%s\" [label=\"%s\" shape=\"%s\"];\n", op.Digest, name, shape)
	}
	for _, op := range ops {
		for i, inp := range op.Op.Inputs {
			label := ""
			if eo, ok := op.Op.Op.(*pb.Op_Exec); ok {
				for _, m := range eo.Exec.Mounts {
					if int(m.Input) == i && m.Dest != "/" {
						label = m.Dest
					}
				}
			}
			fmt.Fprintf(w, "  \"%s\" -> \"%s\" [label=\"%s\"];\n", inp.Digest, op.Digest, label)
		}
	}
}

func attr(dgst digest.Digest, op pb.Op) (string, string) {
	switch op := op.Op.(type) {
	case *pb.Op_Source:
		return op.Source.Identifier, "ellipse"
	case *pb.Op_Exec:
		return strings.Join(op.Exec.Meta.Args, " "), "box"
	default:
		return dgst.String(), "plaintext"
	}
}
