package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

type TarItem struct {
	Header *tar.Header
	Data   []byte
}

func ReadTarToMap(dt []byte, compressed bool) (map[string]*TarItem, error) {
	m := map[string]*TarItem{}
	var r io.Reader = bytes.NewBuffer(dt)
	if compressed {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("error creating gzip reader: %w", err)
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return m, nil
			}
			return nil, fmt.Errorf("error reading tar"+": %w", err)
		}
		if _, ok := m[h.Name]; ok {
			return nil, fmt.Errorf("duplicate entries for %s", h.Name)
		}

		var dt []byte
		if h.Typeflag == tar.TypeReg {
			dt, err = io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("error reading file: %w", err)
			}
		}
		m[h.Name] = &TarItem{Header: h, Data: dt}
	}
}
