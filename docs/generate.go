package main

import (
	"fmt"

	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/util/opts"
)

func main() {
	os, err := opts.Info(&containerimage.ContainerImageCommitOpts{})
	if err != nil {
		panic(err)
	}
	fmt.Println(opts.GenMarkdown(os))
}
