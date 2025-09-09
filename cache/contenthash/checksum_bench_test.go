package contenthash

import (
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	proto "google.golang.org/protobuf/proto"
)

// Buf is used to prevent the benchmark from being optimized away.
var Buf []byte

func BenchmarkMarshalCacheRecords(b *testing.B) {
	v := sampleCacheRecords()
	for b.Loop() {
		var err error
		Buf, err = v.MarshalVT()
		require.NoError(b, err)
	}
}

var CacheRecordsOutput CacheRecords

func BenchmarkUnmarshalCacheRecords(b *testing.B) {
	v := sampleCacheRecords()
	buf, err := proto.Marshal(v)
	require.NoError(b, err)

	for b.Loop() {
		err := CacheRecordsOutput.UnmarshalVT(buf)
		require.NoError(b, err)
	}
}

func sampleCacheRecords() *CacheRecords {
	return &CacheRecords{
		Paths: []*CacheRecordWithPath{
			{
				Path: "/foo",
				Record: &CacheRecord{
					Digest: digest.FromString("/foo").String(),
					Type:   CacheRecordTypeDir,
				},
			},
			{
				Path: "/foo/",
				Record: &CacheRecord{
					Digest: digest.FromString("/foo/").String(),
					Type:   CacheRecordTypeDirHeader,
				},
			},
			{
				Path: "/foo/bar.txt",
				Record: &CacheRecord{
					Digest: digest.FromString("/foo/bar.txt").String(),
					Type:   CacheRecordTypeFile,
				},
			},
			{
				Path: "/foo/link",
				Record: &CacheRecord{
					Digest:   digest.FromString("/foo/link").String(),
					Type:     CacheRecordTypeSymlink,
					Linkname: "/foo/bar.txt",
				},
			},
		},
	}
}
