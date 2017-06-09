package main

import (
	"os"

	"github.com/tonistiigi/buildkit_poc/client/llb"
)

func main() {
	busybox := llb.Image("docker.io/library/busybox:latest")
	mod1 := busybox.Run(llb.Meta{Args: []string{"/bin/sleep", "1"}, Cwd: "/"})
	mod2 := mod1.Run(llb.Meta{Args: []string{"/bin/sh", "-c", "echo foo > /bar"}, Cwd: "/"})
	mod3 := mod2.Run(llb.Meta{Args: []string{"/bin/ls", "-l", "/"}, Cwd: "/"})

	res := mod3
	dt, err := res.Marshal()
	if err != nil {
		panic(err)
	}
	llb.WriteTo(dt, os.Stdout)
}
