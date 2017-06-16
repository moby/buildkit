package main

import (
	"os"

	"github.com/tonistiigi/buildkit_poc/client/llb"
)

func main() {
	busybox := llb.Image("docker.io/library/redis:latest")
	mod1 := busybox.Run(llb.Meta{Args: []string{"/bin/sleep", "1"}, Cwd: "/"})
	mod2 := mod1.Run(llb.Meta{Args: []string{"/bin/sh", "-c", "echo foo > /bar"}, Cwd: "/"})
	alpine := llb.Image("docker.io/library/alpine:latest")
	mod3 := mod2.Run(llb.Meta{Args: []string{"/bin/cp", "-a", "/alpine/etc/passwd", "baz"}, Cwd: "/"})
	mod3.AddMount("/alpine", alpine)
	mod4 := mod3.Run(llb.Meta{Args: []string{"/bin/ls", "-l", "/"}, Cwd: "/"})

	res := mod4
	dt, err := res.Marshal()
	if err != nil {
		panic(err)
	}
	llb.WriteTo(dt, os.Stdout)
}
