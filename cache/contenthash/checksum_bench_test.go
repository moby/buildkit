package contenthash

import (
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// Buf is used to prevent the benchmark from being optimized away.
var Buf []byte

func BenchmarkMarshalCacheRecords(b *testing.B) {
	v := sampleCacheRecords()
	for i := 0; i < b.N; i++ {
		var err error
		Buf, err = v.Marshal()
		require.NoError(b, err)
	}
}

var CacheRecordsOutput CacheRecords

func BenchmarkUnmarshalCacheRecords(b *testing.B) {
	v := sampleCacheRecords()
	buf, err := v.Marshal()
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		err := CacheRecordsOutput.Unmarshal(buf)
		require.NoError(b, err)
	}
}

func sampleCacheRecords() *CacheRecords {
	return &CacheRecords{
		Paths: []*CacheRecordWithPath{
			{
				Path: "/foo",
				Record: &CacheRecord{
					Digest: digest.FromString("/foo"),
					Type:   CacheRecordTypeDir,
				},
			},
			{
				Path: "/foo/",
				Record: &CacheRecord{
					Digest: digest.FromString("/foo/"),
					Type:   CacheRecordTypeDirHeader,
				},
			},
			{
				Path: "/foo/bar.txt",
				Record: &CacheRecord{
					Digest: digest.FromString("/foo/bar.txt"),
					Type:   CacheRecordTypeFile,
				},
			},
			{
				Path: "/foo/link",
				Record: &CacheRecord{
					Digest:   digest.FromString("/foo/link"),
					Type:     CacheRecordTypeSymlink,
					Linkname: "/foo/bar.txt",
				},
			},
		},
	}
}
