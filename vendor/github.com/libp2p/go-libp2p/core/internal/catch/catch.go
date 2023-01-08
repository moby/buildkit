package catch

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

var panicWriter io.Writer = os.Stderr

// HandlePanic handles and logs panics.
func HandlePanic(rerr interface{}, err *error, where string) {
	if rerr != nil {
		fmt.Fprintf(panicWriter, "caught panic: %s\n%s\n", rerr, debug.Stack())
		*err = fmt.Errorf("panic in %s: %s", where, rerr)
	}
}
