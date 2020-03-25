package main

import (
	"context"
	"flag"
	"io/ioutil"
	"log"
	"os"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/appcontext"
)

type buildOpt struct {
	target string
}

func main() {
	var opt buildOpt
	flag.StringVar(&opt.target, "target", "", "target stage")
	flag.Parse()

	df, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}

	caps := pb.Caps.CapSet(pb.Caps.All())

	state, img, err := dockerfile2llb.Dockerfile2LLB(appcontext.Context(), df, dockerfile2llb.ConvertOpt{
		MetaResolver: imagemetaresolver.Default(),
		Target:       opt.target,
		LLBCaps:      &caps,
	})
	if err != nil {
		log.Printf("err: %+v", err)
		panic(err)
	}

	_ = img

	dt, err := state.Marshal(context.TODO())
	if err != nil {
		panic(err)
	}
	llb.WriteTo(dt, os.Stdout)
}
