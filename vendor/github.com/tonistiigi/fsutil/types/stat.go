package types

import (
	"os"

	"google.golang.org/protobuf/proto"
)

func (s *Stat) IsDir() bool {
	return os.FileMode(s.Mode).IsDir()
}

func (s *Stat) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(s)
}

func (s *Stat) Unmarshal(dAtA []byte) error {
	return proto.UnmarshalOptions{Merge: true}.Unmarshal(dAtA, s)
}

func (s *Stat) Clone() *Stat {
	clone := &Stat{
		Path:     s.Path,
		Mode:     s.Mode,
		Uid:      s.Uid,
		Gid:      s.Gid,
		Size:     s.Size,
		ModTime:  s.ModTime,
		Linkname: s.Linkname,
		Devmajor: s.Devmajor,
		Devminor: s.Devminor,
	}
	if s.Xattrs != nil {
		s.Xattrs = make(map[string][]byte, len(s.Xattrs))
		for k, v := range s.Xattrs {
			clone.Xattrs[k] = v
		}
	}
	return clone
}
