package cachedigest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tempDB(t *testing.T) (*DB, func()) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := NewDB(dbPath)
	require.NoError(t, err)
	SetDefaultDB(db) // Ensure defaultDB is set for correct test behavior
	return db, func() {
		db.Close()
		os.RemoveAll(dir)
		SetDefaultDB(&DB{}) // Reset defaultDB after test
	}
}

func TestFromBytesAndGet(t *testing.T) {
	db, cleanup := tempDB(t)
	defer cleanup()

	data := []byte("hello world")
	typ := TypeString

	dgst, err := db.FromBytes(data, typ)
	require.NoError(t, err)
	require.NotEqual(t, digest.Digest(""), dgst)

	db.Wait()

	gotType, frames, err := db.Get(context.Background(), dgst.String())
	require.NoError(t, err)
	require.Equal(t, typ, gotType)

	var foundData bool
	for _, f := range frames {
		if f.ID == FrameIDData {
			require.Equal(t, data, f.Data)
			foundData = true
		}
	}
	require.True(t, foundData, "should find data frame")

	_, _, err = db.Get(context.Background(), digest.FromBytes([]byte("notfound")).String())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestNewHashAndGet(t *testing.T) {
	db, cleanup := tempDB(t)
	defer cleanup()

	h := db.NewHash(TypeStringList)
	inputs := [][]byte{
		[]byte("foo"),
		[]byte("bar"),
	}
	for _, in := range inputs {
		_, err := h.Write(in)
		require.NoError(t, err)
	}
	skip1 := []byte("xxxxx")
	skip2 := []byte("yyy")
	_, err := h.WriteNoDebug(skip1)
	require.NoError(t, err)
	_, err = h.WriteNoDebug(skip2)
	require.NoError(t, err)

	sum := h.Sum()

	db.Wait()

	expectedConcat := slices.Concat(inputs[0], inputs[1], skip1, skip2)
	expectedHash := digest.FromBytes(expectedConcat)
	require.Equal(t, expectedHash, sum, "digest sum should match expected value")

	gotType, frames, err := db.Get(context.Background(), sum.String())
	require.NoError(t, err)
	require.Equal(t, TypeStringList, gotType)

	var dataFrames [][]byte
	var skipLens []uint32
	for _, f := range frames {
		switch f.ID {
		case FrameIDData:
			dataFrames = append(dataFrames, f.Data)
		case FrameIDSkip:
			require.Len(t, f.Data, 4)
			skipLens = append(skipLens, uint32(f.Data[0])<<24|uint32(f.Data[1])<<16|uint32(f.Data[2])<<8|uint32(f.Data[3]))
		}
	}
	require.Len(t, dataFrames, len(inputs))
	for i, in := range inputs {
		require.Equal(t, in, dataFrames[i])
	}
	require.Equal(t, []uint32{uint32(len(skip1) + len(skip2))}, skipLens)
}

func TestEncodeDecodeFrames(t *testing.T) {
	framesIn := []Frame{
		{FrameIDType, []byte(TypeJSON)},
		{FrameIDData, []byte("hello world")},
	}
	encoded, err := encodeFrames(framesIn)
	require.NoError(t, err, "encodeFrames should not error")

	decoded, err := decodeFrames(encoded)
	require.NoError(t, err, "decodeFrames should not error")

	assert.Equal(t, len(framesIn), len(decoded), "number of frames should match")
	for i, f := range framesIn {
		assert.Equal(t, f.ID, decoded[i].ID, "frame id should match")
		assert.Equal(t, f.Data, decoded[i].Data, "frame data should match")
	}
}

func TestDecodeFramesInvalid(t *testing.T) {
	// Too short
	_, err := decodeFrames([]byte{0, 1, 2})
	require.Error(t, err, "should error for short input")

	// Length mismatch
	bad := make([]byte, 12)
	// frameID=1, len=10, but only 4 bytes of data
	bad[0] = 0
	bad[1] = 0
	bad[2] = 0
	bad[3] = 1
	bad[4] = 0
	bad[5] = 0
	bad[6] = 0
	bad[7] = 10
	copy(bad[8:], []byte{1, 2, 3, 4})
	_, err = decodeFrames(bad)
	require.Error(t, err, "should error for length mismatch")
	require.ErrorIs(t, err, ErrInvalidEncoding, "should return ErrInvalidFrameLength")
}

func TestAll(t *testing.T) {
	db, cleanup := tempDB(t)
	defer cleanup()

	records := []struct {
		data []byte
		typ  Type
	}{
		{[]byte("foo"), TypeString},
		{[]byte("bar"), TypeStringList},
		{[]byte("baz"), TypeDigestList},
	}

	var digests []string
	for _, rec := range records {
		dgst, err := db.FromBytes(rec.data, rec.typ)
		require.NoError(t, err)
		digests = append(digests, dgst.String())
	}
	db.Wait()

	found := make(map[string]struct {
		typ    Type
		frames []Frame
	})

	err := db.All(context.TODO(), func(key string, typ Type, frames []Frame) error {
		found[key] = struct {
			typ    Type
			frames []Frame
		}{typ, frames}
		return nil
	})
	require.NoError(t, err)

	require.Len(t, found, len(records))
	for i, rec := range records {
		dgst := digests[i]
		val, ok := found[dgst]
		require.True(t, ok, "digest %s not found", dgst)
		require.Equal(t, rec.typ, val.typ)
		require.Len(t, val.frames, 1)
		require.Equal(t, FrameIDData, val.frames[0].ID)
		require.Equal(t, rec.data, val.frames[0].Data)
	}
}
