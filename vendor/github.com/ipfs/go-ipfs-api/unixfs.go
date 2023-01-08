package shell

import (
	"context"
	"fmt"
)

type UnixLsObject struct {
	Hash  string
	Size  uint64
	Type  string
	Links []*UnixLsLink
}

type UnixLsLink struct {
	Hash string
	Name string
	Size uint64
	Type string
}

type lsOutput struct {
	Objects map[string]*UnixLsObject
}

// FileList entries at the given path using the UnixFS commands
func (s *Shell) FileList(path string) (*UnixLsObject, error) {
	var out lsOutput
	if err := s.Request("file/ls", path).Exec(context.Background(), &out); err != nil {
		return nil, err
	}

	for _, object := range out.Objects {
		return object, nil
	}

	return nil, fmt.Errorf("no object in results")
}
