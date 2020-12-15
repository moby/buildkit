/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package estargz

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"

	"github.com/containerd/stargz-snapshotter/estargz/stargz"
	"github.com/containerd/stargz-snapshotter/util/positionwatcher"
	"github.com/containerd/stargz-snapshotter/util/tempfiles"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

const (
	landmarkContents = 0xf
)

type options struct {
	chunkSize int
}

type Option func(o *options)

func WithChunkSize(chunkSize int) Option {
	return func(o *options) {
		o.chunkSize = chunkSize
	}
}

// Build builds an eStargz blob which is an extended version of stargz, from tar blob passed
// through the argument. If there are some prioritized files are listed in the argument, these
// files are grouped as "prioritized" and can be used for runtime optimization (e.g. prefetch).
// This function builds a blob in parallel, with dividing that blob into several (at least the
// number of runtime.GOMAXPROCS(0)) sub-blobs.
func Build(tarBlob *io.SectionReader, prioritized []string, opt ...Option) (_ io.ReadCloser, _ digest.Digest, rErr error) {
	var opts options
	for _, o := range opt {
		o(&opts)
	}
	layerFiles := tempfiles.NewTempFiles()
	defer func() {
		if rErr != nil {
			if err := layerFiles.CleanupAll(); err != nil {
				rErr = errors.Wrapf(rErr, "failed to cleanup tmp files: %v", err)
			}
		}
	}()
	entries, err := sort(tarBlob, prioritized)
	if err != nil {
		return nil, "", err
	}
	tarParts := divideEntries(entries, runtime.GOMAXPROCS(0))
	sgzParts := make([]*io.SectionReader, len(tarParts))
	var sgzPartsMu sync.Mutex
	var eg errgroup.Group
	for i, parts := range tarParts {
		i, parts := i, parts
		// builds verifiable stargz sub-blobs
		eg.Go(func() error {
			tmp := tempfiles.NewTempFiles()
			defer tmp.CleanupAll()
			tmpFile, err := tmp.TempFile("", "tmpdata")
			if err != nil {
				return err
			}
			sw := stargz.NewWriter(tmpFile)
			sw.ChunkSize = opts.chunkSize
			if err := sw.AppendTar(readerFromEntries(parts...)); err != nil {
				return err
			}
			if err := sw.Close(); err != nil {
				return err
			}
			sgz, err := fileSectionReader(tmpFile)
			if err != nil {
				return err
			}
			r, _, err := newVerifiableStargz(sgz)
			if err != nil {
				return err
			}
			esgzFile, err := layerFiles.TempFile("", "esgzdata")
			if err != nil {
				return err
			}
			if _, err := io.Copy(esgzFile, r); err != nil {
				return err
			}
			sr, err := fileSectionReader(esgzFile)
			if err != nil {
				return err
			}
			sgzPartsMu.Lock()
			sgzParts[i] = sr
			sgzPartsMu.Unlock()
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		rErr = err
		return nil, "", err
	}
	wholeBlob, tocDgst, err := combineBlobs(sgzParts...)
	if err != nil {
		rErr = err
		return nil, "", err
	}
	return readCloser{
		Reader:    wholeBlob,
		closeFunc: layerFiles.CleanupAll,
	}, tocDgst, nil
}

type readCloser struct {
	io.Reader
	closeFunc func() error
}

func (rc readCloser) Close() error {
	return rc.closeFunc()
}

func fileSectionReader(file *os.File) (*io.SectionReader, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return io.NewSectionReader(file, 0, info.Size()), nil
}

// divideEntries divides passed entries to the parts at least the number specified by the
// argument.
func divideEntries(entries []*tarEntry, minPartsNum int) (set [][]*tarEntry) {
	var estimatedSize int64
	for _, e := range entries {
		estimatedSize += e.header.Size
	}
	unitSize := estimatedSize / int64(minPartsNum)
	var (
		nextEnd = unitSize
		offset  int64
	)
	set = append(set, []*tarEntry{})
	for _, e := range entries {
		set[len(set)-1] = append(set[len(set)-1], e)
		offset += e.header.Size
		if offset > nextEnd {
			set = append(set, []*tarEntry{})
			nextEnd += unitSize
		}
	}
	return
}

// sort reads the specified tar blob and returns a list of tar entries.
// If some of prioritized files are specified, the list starts from these
// files with keeping the order specified by the argument.
func sort(in io.ReaderAt, prioritized []string) ([]*tarEntry, error) {

	// Import tar file.
	intar, err := importTar(in)
	if err != nil {
		return nil, errors.Wrap(err, "failed to sort")
	}

	// Sort the tar file respecting to the prioritized files list.
	sorted := &tarFile{}
	for _, l := range prioritized {
		moveRec(l, intar, sorted)
	}
	if len(prioritized) == 0 {
		sorted.add(&tarEntry{
			header: &tar.Header{
				Name:     NoPrefetchLandmark,
				Typeflag: tar.TypeReg,
				Size:     int64(len([]byte{landmarkContents})),
			},
			payload: bytes.NewReader([]byte{landmarkContents}),
		})
	} else {
		sorted.add(&tarEntry{
			header: &tar.Header{
				Name:     PrefetchLandmark,
				Typeflag: tar.TypeReg,
				Size:     int64(len([]byte{landmarkContents})),
			},
			payload: bytes.NewReader([]byte{landmarkContents}),
		})
	}

	// Dump all entry and concatinate them.
	return append(sorted.dump(), intar.dump()...), nil
}

// readerFromEntries returns a reader of tar archive that contains entries passed
// through the arguments.
func readerFromEntries(entries ...*tarEntry) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		defer tw.Close()
		for _, entry := range entries {
			if err := tw.WriteHeader(entry.header); err != nil {
				pw.CloseWithError(fmt.Errorf("Failed to write tar header: %v", err))
				return
			}
			if _, err := io.Copy(tw, entry.payload); err != nil {
				pw.CloseWithError(fmt.Errorf("Failed to write tar payload: %v", err))
				return
			}
		}
		pw.Close()
	}()
	return pr
}

func importTar(in io.ReaderAt) (*tarFile, error) {
	tf := &tarFile{}
	pw, err := positionwatcher.NewPositionWatcher(in)
	if err != nil {
		return nil, errors.Wrap(err, "failed to make position watcher")
	}
	tr := tar.NewReader(pw)

	// Walk through all nodes.
	for {
		// Fetch and parse next header.
		h, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			} else {
				return nil, errors.Wrap(err, "failed to parse tar file")
			}
		}
		if h.Name == PrefetchLandmark || h.Name == NoPrefetchLandmark {
			// Ignore existing landmark
			continue
		}

		// Add entry if not exist.
		if _, ok := tf.get(h.Name); ok {
			return nil, fmt.Errorf("Duplicated entry(%q) is not supported", h.Name)
		}
		tf.add(&tarEntry{
			header:  h,
			payload: io.NewSectionReader(in, pw.CurrentPos(), h.Size),
		})
	}

	return tf, nil
}

func moveRec(name string, in *tarFile, out *tarFile) {
	if name == "" {
		return
	}
	parent, _ := path.Split(strings.TrimSuffix(name, "/"))
	moveRec(parent, in, out)
	if e, ok := in.get(name); ok && e.header.Typeflag == tar.TypeLink {
		moveRec(e.header.Linkname, in, out)
	}
	if e, ok := in.get(name); ok {
		out.add(e)
		in.remove(name)
	}
}

type tarEntry struct {
	header  *tar.Header
	payload io.ReadSeeker
}

type tarFile struct {
	index  map[string]*tarEntry
	stream []*tarEntry
}

func (f *tarFile) add(e *tarEntry) {
	if f.index == nil {
		f.index = make(map[string]*tarEntry)
	}
	f.index[e.header.Name] = e
	f.stream = append(f.stream, e)
}

func (f *tarFile) remove(name string) {
	if f.index != nil {
		delete(f.index, name)
	}
	var filtered []*tarEntry
	for _, e := range f.stream {
		if e.header.Name == name {
			continue
		}
		filtered = append(filtered, e)
	}
	f.stream = filtered
}

func (f *tarFile) get(name string) (e *tarEntry, ok bool) {
	if f.index == nil {
		return nil, false
	}
	e, ok = f.index[name]
	return
}

func (f *tarFile) dump() []*tarEntry {
	return f.stream
}
