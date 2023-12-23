package git

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
)

func runWithStandardUmask(ctx context.Context, cmd *exec.Cmd) error {
	return errors.New("runWithStandardUmask not supported on " + runtime.GOOS)
}
