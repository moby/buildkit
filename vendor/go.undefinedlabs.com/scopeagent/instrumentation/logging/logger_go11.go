// +build !go1.12

package logging

import (
	"io"
	stdlog "log"
	"os"

	"go.undefinedlabs.com/scopeagent/reflection"
)

// Gets the standard logger writer
func getStdLoggerWriter() io.Writer {
	return os.Stderr // There is no way to get the current writer for the standard logger, but the default one is os.Stderr
}

// Gets the writer of a custom logger
func getLoggerWriter(logger *stdlog.Logger) io.Writer {
	// There is not API in Go1.11 to get the current writer, accessing by reflection.
	if ptr, err := reflection.GetFieldPointerOf(logger, "out"); err == nil {
		return *(*io.Writer)(ptr)
	}
	return nil
}
