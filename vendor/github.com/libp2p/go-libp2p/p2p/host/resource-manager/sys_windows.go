//go:build windows

package rcmgr

import (
	"math"
)

func getNumFDs() int {
	return math.MaxInt
}
