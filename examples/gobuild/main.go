// +build ignore

package main

import (
	"os"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/examples/gobuilder"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

func run() error {

	src := llb.Local("src")

	g := gobuilder.New(src)

	sc := llb.Scratch().
		// With(copyAll(g.Build("github.com/moby/buildkit/client/llb"), "/"))
		With(copyAll(g.Build("github.com/moby/buildkit/cmd/buildd"), "/")).
		With(copyAll(g.Build("github.com/moby/buildkit/cmd/buildctl"), "/"))

	dt, err := sc.Marshal()
	if err != nil {
		panic(err)
	}
	llb.WriteTo(dt, os.Stdout)
	return nil
}

func copyAll(src llb.State, destPath string) llb.StateOption {
	return copyFrom(src, "/.", destPath)
}

// copyFrom has similar semantics as `COPY --from`
func copyFrom(src llb.State, srcPath, destPath string) llb.StateOption {
	return func(s llb.State) llb.State {
		return copy(src, srcPath, s, destPath)
	}
}

// copy copies files between 2 states using cp until there is no copyOp
func copy(src llb.State, srcPath string, dest llb.State, destPath string) llb.State {
	cpImage := llb.Image("docker.io/library/alpine@sha256:1072e499f3f655a032e88542330cf75b02e7bdf673278f701d7ba61629ee3ebe")
	cp := cpImage.Run(llb.Shlexf("cp -a /src%s /dest%s", srcPath, destPath))
	cp.AddMount("/src", src, llb.Readonly)
	return cp.AddMount("/dest", dest)
}
