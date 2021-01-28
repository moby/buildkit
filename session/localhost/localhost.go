package localhost

import (
	"context"
	"fmt"
	"io"

	"github.com/moby/buildkit/session"
)

// RunOnLocalHostMagicStr is a magic mount path which is used to signal the RUN command
// should be run locally whenever this mount path exists; the contents of the mounted path is irrelevant.
const RunOnLocalHostMagicStr = "/run_on_localhost_271c67a1-94d9-4241-8bca-cbae334622ae"

// LocalhostExec is called by buildkitd; it connects to the user's client to request the client execute a command localy.
func LocalhostExec(ctx context.Context, c session.Caller, args []string, stdout, stderr io.Writer) error {
	// stdout and stderr get closed in execOp.Exec()

	client := NewLocalhostClient(c.Conn())
	stream, err := client.Exec(ctx)
	if err != nil {
		return err
	}

	req := InputMessage{
		Command: args,
	}
	if err := stream.SendMsg(&req); err != nil {
		return err
	}

	var exitCodeSet bool
	var exitCode int
	for {
		var msg OutputMessage
		err := stream.RecvMsg(&msg)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		stdout.Write(msg.Stdout)
		stderr.Write(msg.Stderr)
		switch msg.Status {
		case RUNNING:
			//ignore
		case DONE:
			if exitCodeSet {
				panic("received multiple DONE messages (shouldn't happen)")
			}
			exitCode = int(msg.ExitCode)
			exitCodeSet = true
		default:
			return fmt.Errorf("unhandled exit status: %d", msg.Status)
		}
	}

	if exitCode != 0 {
		return fmt.Errorf("exit code: %d", exitCode)
	}

	return nil
}
