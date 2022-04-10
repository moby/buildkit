package main

import (
	"context"
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
	target string
}

func main() {
	if err := xmain(); err != nil {
		logrus.Fatal(err)
	}
}

func xmain() error {
	var opt buildOpt
	flag.StringVar(&opt.target, "target", "", "target stage")
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

	_ = img
	_ = bi

	dt, err := state.Marshal(context.TODO())
	if err != nil {
		return err
	}
	return llb.WriteTo(dt, os.Stdout)
}
