package contenthash

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/cache"
)

type saveTestMetadata struct {
	cache.RefMetadata
	mu     sync.Mutex
	data   []byte
	writes int
}

func (m *saveTestMetadata) GetExternal(string) ([]byte, error) { return nil, nil }
func (m *saveTestMetadata) SetExternal(_ string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append(m.data[:0], data...)
	m.writes++
	return nil
}

// Keep one producer on a single P so its short batch can queue saves before
// the background work is scheduled. No snapshots, mounts, or daemon are needed.
func TestChecksumSaveCoalescing(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	md := &saveTestMetadata{}
	cc, err := newCacheContext(md)
	if err != nil {
		t.Fatal(err)
	}
	const count = 64
	txn := cc.tree.Txn()
	for i := range count {
		p := fmt.Sprintf("d%03d", i)
		txn.Insert(convertPathToKey("/"+p), &CacheRecord{Type: CacheRecordTypeDir})
		txn.Insert(convertPathToKey("/"+p+"/"), &CacheRecord{Type: CacheRecordTypeDirHeader, Digest: string(dgstFileData0)})
		txn.Insert(convertPathToKey("/"+p+"/file"), &CacheRecord{Type: CacheRecordTypeFile, Digest: string(dgstFileData0)})
	}
	cc.tree = txn.Commit()
	for i := range count {
		if _, err := cc.lazyChecksum(context.Background(), nil, fmt.Sprintf("d%03d", i), false); err != nil {
			t.Fatal(err)
		}
	}
	var dump bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&dump, 2); err != nil {
		t.Fatal(err)
	}
	queued := strings.Count(dump.String(), "created by github.com/moby/buildkit/cache/contenthash.(*cacheContext).")
	// A synchronous save waits for any queued owner of the same mutex, then
	// lets the test inspect the final persisted content rather than a partial tree.
	if err := cc.save(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	md.mu.Lock()
	var records CacheRecords
	err = records.UnmarshalVT(md.data)
	writes := md.writes
	md.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	complete := 0
	for _, p := range records.Paths {
		if p.Record.Type == CacheRecordTypeDir && p.Record.Digest != "" {
			complete++
		}
	}
	t.Logf("queued save goroutines=%d metadata writes=%d persisted directory checksums=%d", queued, writes, complete)
	if complete != count {
		t.Fatalf("persisted %d checksums, want %d", complete, count)
	}
	if queued > 1 {
		t.Fatalf("queued %d redundant saves for one context, want at most one", queued)
	}
}
