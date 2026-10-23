package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/moby/buildkit/util/testutil/sshutil/opensshcheck"
	"github.com/pkg/errors"
)

func main() {
	var cfg opensshcheck.Config
	flag.StringVar(&cfg.SSHPath, "ssh", "", "path to Windows ssh.exe")
	flag.StringVar(&cfg.Expected, "expected", "", "base64 SSH public key blob")
	flag.StringVar(&cfg.Output, "output", "", "result path")
	flag.Parse()
	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Minute, errors.New("Windows OpenSSH check timed out"))
	err := opensshcheck.Run(ctx, cfg)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
