// +build go1.12
// +build !go1.13

package logging

import (
	"io"
	stdlog "log"
	"os"
)

// Gets the standard logger writer
func getStdLoggerWriter() io.Writer {
	return os.Stderr // There is no way to get the current writer for the standard logger, but the default one is os.Stderr
}

// Gets the writer of a custom logger
func getLoggerWriter(logger *stdlog.Logger) io.Writer {
	return logger.Writer()
}
