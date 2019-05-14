// +build !linux

package spawner

import (
	"errors"
)

func New(flagsStr string) (Spawner, error) {
	return nil, errors.New("spawn only supported on linux")
}
