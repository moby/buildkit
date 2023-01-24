//go:build gofuzz
// +build gofuzz

package multistream

import "bytes"

type rwc struct {
	*bytes.Reader
}

func (*rwc) Write(b []byte) (int, error) {
	return len(b), nil
}

func (*rwc) Close() error {
	return nil
}

func Fuzz(b []byte) int {
	readStream := bytes.NewReader(b)
	input := &rwc{readStream}

	mux := NewMultistreamMuxer()
	mux.AddHandler("/a", nil)
	mux.AddHandler("/b", nil)
	_ = mux.Handle(input)
	return 1
}
