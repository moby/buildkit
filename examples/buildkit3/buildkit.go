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
	local      bool
}

func main() {
	var opt buildOpt
	flag.StringVar(&opt.target, "target", "containerd", "target (standalone, containerd)")
	flag.StringVar(&opt.containerd, "containerd", "master", "containerd version")
	flag.StringVar(&opt.runc, "runc", "v1.0.0-rc3", "runc version")
	flag.BoolVar(&opt.local, "local", false, "use local buildkit source")
	flag.Parse()

	bk := buildkit(opt)
	out := bk.Run(llb.Shlex("ls -l /bin")) // debug output

	dt, err := out.Marshal()
	if err != nil {
		panic(err)
	}
	llb.WriteTo(dt, os.Stdout)
}

func goBuildBase() *llb.State {
	goAlpine := llb.Image("docker.io/library/golang:1.8-alpine")
	return goAlpine.
		AddEnv("PATH", "/usr/local/go/bin:"+system.DefaultPathEnv).
		AddEnv("GOPATH", "/go").
		Run(llb.Shlex("apk add --no-cache g++ linux-headers make")).Root()
}

func goRepo(s *llb.State, repo string, src *llb.State) func(ro ...llb.RunOption) *llb.State {
	dir := "/go/src/" + repo
	return func(ro ...llb.RunOption) *llb.State {
		es := s.Dir(dir).Run(ro...)
		es.AddMount(dir, src, llb.Readonly)
		return es.AddMount("/out", llb.Scratch())
	}
}

func runc(version string) *llb.State {
	repo := "github.com/opencontainers/runc"
	return goRepo(goBuildBase(), repo, llb.Git(repo, version))(
		llb.Shlex("go build -o /out/runc ./"),
	)
}

func containerd(version string) *llb.State {
	repo := "github.com/containerd/containerd"
	return goRepo(
		goBuildBase().
			Run(llb.Shlex("apk add --no-cache btrfs-progs-dev")).Root(),
		repo, llb.Git(repo, version, llb.KeepGitDir()))(
		llb.Shlex("go build -o /out/containerd ./cmd/containerd"),
	)
}

func buildkit(opt buildOpt) *llb.State {
	repo := "github.com/moby/buildkit"
	src := llb.Git(repo, "master")
	if opt.local {
		src = llb.Local("buildkit-src")
	}
	run := goRepo(goBuildBase(), repo, src)

	builddStandalone := run(llb.Shlex("go build -o /out/buildd-standalone -tags standalone ./cmd/buildd"))

	builddContainerd := run(llb.Shlex("go build -o /out/buildd-containerd -tags containerd ./cmd/buildd"))

	buildctl := run(llb.Shlex("go build -o /out/buildctl ./cmd/buildctl"))

	r := llb.Image("docker.io/library/alpine:latest").With(
		copyAll(buildctl, "/bin"),
		copyAll(runc(opt.runc), "/bin"),
	)

	if opt.target == "containerd" {
		return r.With(
			copyAll(containerd(opt.containerd), "/bin"),
			copyAll(builddContainerd, "/bin"))
	}
	return r.With(copyAll(builddStandalone, "/bin"))
}

func copyAll(src *llb.State, destPath string) llb.StateOption {
	return copyFrom(src, "/.", destPath)
}

// copyFrom has similar semantics as `COPY --from`
func copyFrom(src *llb.State, srcPath, destPath string) llb.StateOption {
	return func(s *llb.State) *llb.State {
		return copy(src, srcPath, s, destPath)
	}
}

// copy copies files between 2 states using cp until there is no copyOp
func copy(src *llb.State, srcPath string, dest *llb.State, destPath string) *llb.State {
	cpImage := llb.Image("docker.io/library/alpine:latest")
	cp := cpImage.Run(llb.Shlexf("cp -a /src%s /dest%s", srcPath, destPath))
	cp.AddMount("/src", src)
	return cp.AddMount("/dest", dest)
}
