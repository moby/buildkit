package compression

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGo126GzipWriter(t *testing.T) {
	data := bytes.Repeat([]byte("BuildKit gzip compatibility data\x00with repetition\n"), 4096)
	// These digests were produced by compress/gzip from Go 1.26.5.
	expected := map[int]string{
		-1: "b9738544854e7f362743dabbe03fac154d2b62cb7e705781dcc4456583bec7dc",
		0:  "0069b765e18ce96a96cee8d1ea6f7968177081057cb34da7f7b8d5c0d9e19628",
		1:  "e356453bad442c4eb03a0b87efd9e335d983ebb3c1eca0a64b2e1e22e628438e",
		6:  "b9738544854e7f362743dabbe03fac154d2b62cb7e705781dcc4456583bec7dc",
		9:  "79f9d5915049926bfe979ec91245ab7b9ac469172e5a539c135e571e3b2b4a04",
	}

	for level, digest := range expected {
		t.Run(strconv.Itoa(level), func(t *testing.T) {
			var compressed bytes.Buffer
			w, err := gzipWriter(Config{Level: &level})(&compressed)
			require.NoError(t, err)
			_, err = w.Write(data)
			require.NoError(t, err)
			require.NoError(t, w.Close())

			sum := sha256.Sum256(compressed.Bytes())
			require.Equal(t, digest, fmt.Sprintf("%x", sum))

			r, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
			require.NoError(t, err)
			uncompressed, err := io.ReadAll(r)
			require.NoError(t, err)
			require.NoError(t, r.Close())
			require.Equal(t, data, uncompressed)
		})
	}
}
