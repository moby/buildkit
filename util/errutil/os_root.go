package errutil

import (
	"errors"
	"os"
	"strings"
)

func IsPathEscapesRootError(err error) bool {
	var pe *os.PathError
	if !errors.As(err, &pe) {
		return false
	}
	return strings.Contains(pe.Err.Error(), "path escapes")
}
