package debug

import (
	"context"

	"github.com/urfave/cli/v3"
)

func commandAction(fn func(*cli.Command) error) cli.ActionFunc {
	return func(_ context.Context, cmd *cli.Command) error {
		return fn(cmd)
	}
}
