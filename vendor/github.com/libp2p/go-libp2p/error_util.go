package libp2p

import (
	"fmt"
	"runtime"
)

func traceError(err error, skip int) error {
	if err == nil {
		return nil
	}
	_, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return err
	}
	return fmt.Errorf("%s:%d: %s", file, line, err)
}
