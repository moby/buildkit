package boltutil

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"slices"

	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

// inspectFragmentation runs with the transaction gate paused and hmu held.
func (d *DB) inspectFragmentation(ctx context.Context) (int64, error) {
	f, err := os.Open(d.path) // #nosec G703 -- Database paths are daemon configuration, not build input.
	if err != nil {
		return 0, errors.WithStack(err)
	}
	defer f.Close()
	var reclaimable int64
	err = d.bdb.View(func(tx *bolt.Tx) error {
		var roots []uint64
		if err := tx.ForEach(func(_ []byte, b *bolt.Bucket) error {
			if id := uint64(b.Root()); id != 0 {
				roots = append(roots, id)
			}
			return context.Cause(ctx)
		}); err != nil {
			return err
		}
		var err error
		reclaimable, err = fragmentedBytes(ctx, f, int64(d.pageSize), tx.Size(), roots)
		return err
	})
	return reclaimable, err
}

// bbolt's Stats cannot be canceled. Read only the page and element headers needed
// for leaf occupancy and child references, checking cancellation between reads.
// The native-endian layout is defined in bbolt v1.5.0:
// https://github.com/etcd-io/bbolt/blob/e7a8b2dd498494a3766ba24dd94d3509e5588485/internal/common/page.go
// https://github.com/etcd-io/bbolt/blob/e7a8b2dd498494a3766ba24dd94d3509e5588485/internal/common/bucket.go
func fragmentedBytes(ctx context.Context, r io.ReaderAt, pageSize, size int64, roots []uint64) (int64, error) {
	const headerSize = 16 // Page, element, and bucket headers are each 16 bytes.
	const branchPage, leafPage, bucketElement = 1, 2, 1
	order := binary.NativeEndian
	limit := uint64(size / pageSize)
	seen := make(map[uint64]struct{})
	var header [headerSize]byte
	var elements []byte
	var allocated, slack int64
	reserved := int64(len(roots)) * pageSize
	read := func(buf []byte, offset int64) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		_, err := r.ReadAt(buf, offset)
		return errors.WithStack(err)
	}
	for len(roots) > 0 {
		id := roots[len(roots)-1]
		roots = roots[:len(roots)-1]
		if id < 2 || id >= limit {
			return 0, errors.Errorf("fragmentation scan: page %d out of bounds", id)
		}
		if _, ok := seen[id]; ok {
			return 0, errors.Errorf("fragmentation scan: repeated page %d", id)
		}
		seen[id] = struct{}{}
		offset := int64(id) * pageSize
		if err := read(header[:], offset); err != nil {
			return 0, err
		}
		flags := order.Uint16(header[8:10])
		count := int(order.Uint16(header[10:12]))
		overflow := uint64(order.Uint32(header[12:16]))
		if order.Uint64(header[:8]) != id || (flags != branchPage && flags != leafPage) || overflow >= limit-id {
			return 0, errors.Errorf("fragmentation scan: invalid page %d", id)
		}
		allocation := int64(overflow+1) * pageSize
		tableEnd := int64(headerSize + count*headerSize)
		if tableEnd > allocation {
			return 0, errors.Errorf("fragmentation scan: invalid element count on page %d", id)
		}
		elements = slices.Grow(elements[:0], count*headerSize)[:count*headerSize]
		if len(elements) > 0 {
			if err := read(elements, offset+headerSize); err != nil {
				return 0, err
			}
		}
		pageUsed := int64(headerSize)
		for i := range count {
			if err := context.Cause(ctx); err != nil {
				return 0, err
			}
			e := elements[i*headerSize : (i+1)*headerSize]
			if flags == branchPage {
				roots = append(roots, order.Uint64(e[8:16]))
				continue
			}
			start := int64(headerSize+i*headerSize) + int64(order.Uint32(e[4:8]))
			keySize, valueSize := int64(order.Uint32(e[8:12])), int64(order.Uint32(e[12:16]))
			end := start + keySize + valueSize
			if start < tableEnd || end > allocation || start < pageUsed {
				return 0, errors.Errorf("fragmentation scan: invalid element on page %d", id)
			}
			pageUsed = end
			if order.Uint32(e[:4])&bucketElement != 0 {
				if valueSize < headerSize {
					return 0, errors.Errorf("fragmentation scan: invalid bucket on page %d", id)
				}
				if err := read(header[:], offset+start+keySize); err != nil {
					return 0, err
				}
				if child := order.Uint64(header[:8]); child != 0 {
					roots = append(roots, child)
					reserved += pageSize
				}
			}
		}
		if flags == leafPage {
			if allocation > size-allocated {
				return 0, errors.New("fragmentation scan: leaf allocation exceeds database size")
			}
			allocated += allocation
			if overflow == 0 {
				slack += allocation - pageUsed
			}
		}
	}
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	// Exclude overflow leaves' tail padding and reserve one partially filled page
	// per external bucket. Large values must not cancel slack in other pages.
	return max(slack-reserved, 0), nil
}
