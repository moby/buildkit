package llb

import (
	"encoding/binary"
	"io"
)

// These are temporary functions for testing the solver without frontends

func WriteTo(dt [][]byte, w io.Writer) error {
	for _, d := range dt {
		l := make([]byte, 4)
		binary.LittleEndian.PutUint32(l, uint32(len(d)))
		if _, err := w.Write(l); err != nil {
			return err
		}
		if _, err := w.Write(d); err != nil {
			return err
		}
	}
	return nil
}

func ReadFrom(r io.Reader) (out [][]byte, err error) {
	for {
		b := make([]byte, 4)
		if n, err := io.ReadFull(r, b); err != nil {
			if err == io.EOF && n == 0 {
				return out, nil
			}
			return nil, err
		}
		l := binary.LittleEndian.Uint32(b)
		b = make([]byte, l)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
}
