package main

import (
	"log"
	"os"

	"github.com/moby/buildkit/client/llb"
)

func main() {
	busybox := llb.Image("docker.io/library/busybox:latest")
	img1 := busybox.
		Run(llb.Shlex("sleep 1")).
		Run(llb.Shlex("sh -c \"echo foo > /bar\""))

	alpine := llb.Image("docker.io/library/alpine:latest")

	copy := img1.Run(llb.Shlex("cp -a /alpine/etc/passwd /baz"))
	copy.AddMount("/alpine", alpine)
	copy.AddMount("/subroot", busybox)

	res := copy.Run(llb.Shlex("ls -l /"))

	dt, err := res.Marshal()
	if err != nil {
		log.Printf("%+v\n", err)
		panic(err)
	}
	llb.WriteTo(dt, os.Stdout)
}
