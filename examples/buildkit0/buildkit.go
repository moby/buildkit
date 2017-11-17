package main

import (
	"flag"
	"os"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/system"
)

type buildOpt struct {
	target     string
	containerd string
	runc       string
}

func main() {
	var opt buildOpt
	flag.StringVar(&opt.target, "target", "containerd", "target (standalone, containerd)")
	flag.StringVar(&opt.containerd, "containerd", "9649a428e8c470cddc0d5a3c394f36111f940adc", "containerd version")
	flag.StringVar(&opt.runc, "runc", "74a17296470088de3805e138d3d87c62e613dfc4", "runc version")
	flag.Parse()

	bk := buildkit(opt)
	out := bk.Run(llb.Shlex("ls -l /bin")) // debug output

	dt, err := out.Marshal()
	if err != nil {
		panic(err)
	}
	llb.WriteTo(dt, os.Stdout)
}

func goBuildBase() llb.State {
	goAlpine := llb.Image("docker.io/library/golang:1.9-alpine")
	return goAlpine.
		AddEnv("PATH", "/usr/local/go/bin:"+system.DefaultPathEnv).
		AddEnv("GOPATH", "/go").
		Run(llb.Shlex("apk add --no-cache g++ linux-headers")).
		Run(llb.Shlex("apk add --no-cache git make")).Root()
}

func runc(version string) llb.State {
	return goBuildBase().
		Run(llb.Shlex("git clone https://github.com/opencontainers/runc.git /go/src/github.com/opencontainers/runc")).
		Dir("/go/src/github.com/opencontainers/runc").
		Run(llb.Shlexf("git checkout -q %s", version)).
		Run(llb.Shlex("go build -o /usr/bin/runc ./")).Root()
}

func containerd(version string) llb.State {
	return goBuildBase().
		Run(llb.Shlex("apk add --no-cache btrfs-progs-dev")).
		Run(llb.Shlex("git clone https://github.com/containerd/containerd.git /go/src/github.com/containerd/containerd")).
		Dir("/go/src/github.com/containerd/containerd").
		Run(llb.Shlexf("git checkout -q %s", version)).
		Run(llb.Shlex("make bin/containerd")).Root()
}

func buildkit(opt buildOpt) llb.State {
	src := goBuildBase().
		Run(llb.Shlex("git clone https://github.com/moby/buildkit.git /go/src/github.com/moby/buildkit")).
		Dir("/go/src/github.com/moby/buildkit")

	builddStandalone := src.
		Run(llb.Shlex("go build -o /bin/buildd-standalone -tags standalone ./cmd/buildd"))

	builddContainerd := src.
		Run(llb.Shlex("go build -o /bin/buildd-containerd -tags containerd ./cmd/buildd"))

	buildctl := src.
		Run(llb.Shlex("go build -o /bin/buildctl ./cmd/buildctl"))

	r := llb.Image("docker.io/library/alpine:latest")
	r = copy(buildctl.Root(), "/bin/buildctl", r, "/bin/")
	r = copy(runc(opt.runc), "/usr/bin/runc", r, "/bin/")
	if opt.target == "containerd" {
		r = copy(containerd(opt.containerd), "/go/src/github.com/containerd/containerd/bin/containerd", r, "/bin/")
		r = copy(builddContainerd.Root(), "/bin/buildd-containerd", r, "/bin/")
	} else {
		r = copy(builddStandalone.Root(), "/bin/buildd-standalone", r, "/bin/")
	}
	return r
}

func copy(src llb.State, srcPath string, dest llb.State, destPath string) llb.State {
	cpImage := llb.Image("docker.io/library/alpine:latest")
	cp := cpImage.Run(llb.Shlexf("cp -a /src%s /dest%s", srcPath, destPath))
	cp.AddMount("/src", src)
	return cp.AddMount("/dest", dest)
}
