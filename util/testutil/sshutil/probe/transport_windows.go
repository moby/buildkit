package probe

import (
	"context"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/pkg/errors"
	"golang.org/x/sys/windows"
)

func Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, time.Second, errors.New("SSH pipe dial timed out"))
	defer cancel()
	return winio.DialPipeContext(ctx, endpoint)
}

func defaultEndpoint() string { return `\\.\pipe\openssh-ssh-agent` }

func isAbsent(err error) bool { return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) }

func checkDefaults(r Report) error {
	if r.AuthSock != "" || r.Endpoint != defaultEndpoint() {
		return errors.Errorf("unexpected Windows SSH defaults: %+v", r)
	}
	return nil
}
