// +build go1.13

package logging

import (
	"io"
	stdlog "log"
)

// Gets the standard logger writer
func getStdLoggerWriter() io.Writer {
	return stdlog.Writer() // This func exist from go1.13
}

// Gets the writer of a custom logger
func getLoggerWriter(logger *stdlog.Logger) io.Writer {
	return logger.Writer()
}
