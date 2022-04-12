package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/sirupsen/logrus"
)

type buildOpt struct {
	target                 string
	partialImageConfigFile string
	partialMetadataFile    string
}

func main() {
	if err := xmain(); err != nil {
		logrus.Fatal(err)
	}
}

func xmain() error {
	var opt buildOpt
	flag.StringVar(&opt.target, "target", "", "target stage")
	flag.StringVar(&opt.partialImageConfigFile, "partial-image-config-file", "", "Output partial image config as a JSON file")
	flag.StringVar(&opt.partialMetadataFile, "partial-metadata-file", "", "Output partial metadata sa a JSON file")
	flag.Parse()

	df, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}

	caps := pb.Caps.CapSet(pb.Caps.All())

	state, img, bi, err := dockerfile2llb.Dockerfile2LLB(appcontext.Context(), df, dockerfile2llb.ConvertOpt{
		MetaResolver: imagemetaresolver.Default(),
		Target:       opt.target,
		LLBCaps:      &caps,
	})
	if err != nil {
		return err
	}

	dt, err := state.Marshal(context.TODO())
	if err != nil {
		return err
	}
	if err := llb.WriteTo(dt, os.Stdout); err != nil {
		return err
	}
	if opt.partialImageConfigFile != "" {
		if err := writeJSON(opt.partialImageConfigFile, img); err != nil {
			return err
		}
	}
	if opt.partialMetadataFile != "" {
		if err := writeJSON(opt.partialMetadataFile, bi); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(f string, x interface{}) error {
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(f); err != nil {
		return err
	}
	return os.WriteFile(f, b, 0o644)
}
