package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/moby/buildkit/util/testutil/sshutil/probe"
	"github.com/pkg/errors"
)

func main() {
	var cfg probe.Config
	var output string
	flag.StringVar(&cfg.Endpoint, "endpoint", "", "override agent endpoint")
	flag.StringVar(&cfg.Expected, "expected", "", "base64 SSH public key blob")
	flag.BoolVar(&cfg.Absent, "absent", false, "require endpoint to be absent")
	flag.BoolVar(&cfg.Mutate, "mutate", false, "require mutation rejection")
	flag.BoolVar(&cfg.Defaults, "defaults", true, "check platform endpoint defaults")
	flag.IntVar(&cfg.Cycles, "cycles", 1, "sequential connections")
	flag.StringVar(&output, "output", "", "JSON report path")
	flag.Parse()
	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Minute, errors.New("SSH probe timed out"))
	r, err := probe.Run(ctx, cfg)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dt, err := json.Marshal(r)
	if err == nil {
		if output == "" {
			_, err = os.Stdout.Write(append(dt, '\n'))
		} else {
			err = os.WriteFile(output, dt, 0600)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
