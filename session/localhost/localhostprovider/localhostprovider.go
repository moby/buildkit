package localhostprovider

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/localhost"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func NewLocalhostProvider() (session.Attachable, error) {
	return &localhostProvider{
		m: &sync.Mutex{},
	}, nil
}

type localhostProvider struct {
	m *sync.Mutex
}

func (lp *localhostProvider) Register(server *grpc.Server) {
	localhost.RegisterLocalhostServer(server, lp)
}

func (lp *localhostProvider) Exec(stream localhost.Localhost_ExecServer) error {
	ctx := stream.Context()
	opts, _ := metadata.FromIncomingContext(ctx)
	_ = opts // opts aren't used for anything at the moment

	// first message must contain the command (and no stdin)
	var msg localhost.InputMessage
	err := stream.RecvMsg(&msg)
	if err != nil {
		return err
	}

	// it might be possible to run in parallel; but it hasn't been tested.
	lp.m.Lock()
	defer lp.m.Unlock()

	if len(msg.Command) == 0 {
		return fmt.Errorf("command is empty")
	}
	cmdStr := msg.Command[0]
	args := msg.Command[1:]

	cmd := exec.CommandContext(ctx, cmdStr, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	err = cmd.Start()
	if err != nil {
		return err
	}

	m := sync.Mutex{}
	var wg sync.WaitGroup
	wg.Add(2)
	const readSize = 8196
	var readErr []error
	go func() {
		defer wg.Done()
		for buf := make([]byte, readSize); ; {
			n, err := stdout.Read(buf)
			if n > 0 {
				m.Lock()
				resp := localhost.OutputMessage{
					Stdout: buf[:n],
				}
				err := stream.SendMsg(&resp)
				if err != nil {
					readErr = append(readErr, err)
					m.Unlock()
					return
				}
				m.Unlock()
			}
			if err != nil {
				m.Lock()
				if err != io.EOF {
					readErr = append(readErr, err)
				}
				m.Unlock()
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for buf := make([]byte, readSize); ; {
			n, err := stderr.Read(buf)
			if n > 0 {
				m.Lock()
				resp := localhost.OutputMessage{
					Stderr: buf[:n],
				}
				err := stream.SendMsg(&resp)
				if err != nil {
					readErr = append(readErr, err)
					m.Unlock()
					return
				}
				m.Unlock()
			}
			if err != nil {
				m.Lock()
				if err != io.EOF {
					readErr = append(readErr, err)
				}
				m.Unlock()
				return
			}
		}
	}()

	wg.Wait()
	if len(readErr) != 0 {
		for _, err := range readErr {
			fmt.Fprintf(os.Stderr, "got error while reading from locally-run process: %v\n", err)
		}
		return readErr[0]
	}

	var exitCode int
	status := localhost.DONE
	err = cmd.Wait()
	if err != nil {
		if exiterr, ok := err.(*exec.ExitError); ok {
			if waitStatus, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				exitCode = waitStatus.ExitStatus()
			} else {
				status = localhost.KILLED
			}
		} else {
			status = localhost.KILLED
		}
		return err
	}

	resp := localhost.OutputMessage{
		ExitCode: int32(exitCode),
		Status:   status,
	}
	if err := stream.SendMsg(&resp); err != nil {
		return err
	}

	return nil
}
