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

// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The stargz package reads & writes tar.gz ("tarball") files in a
// seekable, indexed format call "stargz". A stargz file is still a
// valid tarball, but it's slightly bigger with new gzip streams for
// each new file & throughout large files, and has an index in a magic
// file at the end.

package stargz

// Low-level components for building/parsing eStargz.
// This is the modified version of stargz library (https://github.com/google/crfs)
// following eStargz specification.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
)

// TOCTarName is the name of the JSON file in the tar archive in the
// table of contents gzip stream.
const TOCTarName = "stargz.index.json"

// FooterSize is the number of bytes in the footer
//
// The footer is an empty gzip stream with no compression and an Extra
// header of the form "%016xSTARGZ", where the 64 bit hex-encoded
// number is the offset to the gzip stream of JSON TOC.
//
// 51 comes from:
//
// 10 bytes  gzip header
// 2  bytes  XLEN (length of Extra field) = 26 (4 bytes header + 16 hex digits + len("STARGZ"))
// 2  bytes  Extra: SI1 = 'S', SI2 = 'G'
// 2  bytes  Extra: LEN = 22 (16 hex digits + len("STARGZ"))
// 22 bytes  Extra: subfield = fmt.Sprintf("%016xSTARGZ", offsetOfTOC)
// 5  bytes  flate header
// 8  bytes  gzip footer
// (End of the eStargz blob)
//
// NOTE: For Extra fields, subfield IDs SI1='S' SI2='G' is used for eStargz.
const FooterSize = 51

// legacyFooterSize is the number of bytes in the legacy stargz footer.
//
// 47 comes from:
//
//   10 byte gzip header +
//   2 byte (LE16) length of extra, encoding 22 (16 hex digits + len("STARGZ")) == "\x16\x00" +
//   22 bytes of extra (fmt.Sprintf("%016xSTARGZ", tocGzipOffset))
//   5 byte flate header
//   8 byte gzip footer (two little endian uint32s: digest, size)
const legacyFooterSize = 47

// A Reader permits random access reads from a stargz file.
type Reader struct {
	sr  *io.SectionReader
	toc *jtoc

	// m stores all non-chunk entries, keyed by name.
	m map[string]*TOCEntry

	// chunks stores all TOCEntry values for regular files that
	// are split up. For a file with a single chunk, it's only
	// stored in m.
	chunks map[string][]*TOCEntry
}

// Open opens a stargz file for reading.
func Open(sr *io.SectionReader) (*Reader, error) {
	tocOff, footerSize, err := OpenFooter(sr)
	if err != nil {
		return nil, errors.Wrapf(err, "error parsing footer")
	}
	tocTargz := make([]byte, sr.Size()-tocOff-footerSize)
	if _, err := sr.ReadAt(tocTargz, tocOff); err != nil {
		return nil, fmt.Errorf("error reading %d byte TOC targz: %v", len(tocTargz), err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(tocTargz))
	if err != nil {
		return nil, fmt.Errorf("malformed TOC gzip header: %v", err)
	}
	zr.Multistream(false)
	tr := tar.NewReader(zr)
	h, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("failed to find tar header in TOC gzip stream: %v", err)
	}
	if h.Name != TOCTarName {
		return nil, fmt.Errorf("TOC tar entry had name %q; expected %q", h.Name, TOCTarName)
	}
	toc := new(jtoc)
	if err := json.NewDecoder(tr).Decode(&toc); err != nil {
		return nil, fmt.Errorf("error decoding TOC JSON: %v", err)
	}
	r := &Reader{sr: sr, toc: toc}
	if err := r.initFields(); err != nil {
		return nil, fmt.Errorf("failed to initialize fields of entries: %v", err)
	}
	return r, nil
}

// OpenFooter extracts and parses footer from the given blob.
func OpenFooter(sr *io.SectionReader) (tocOffset int64, footerSize int64, rErr error) {
	if sr.Size() < FooterSize && sr.Size() < legacyFooterSize {
		return 0, 0, fmt.Errorf("blob size %d is smaller than the footer size", sr.Size())
	}
	// TODO: read a bigger chunk (1MB?) at once here to hopefully
	// get the TOC + footer in one go.
	var footer [FooterSize]byte
	if _, err := sr.ReadAt(footer[:], sr.Size()-FooterSize); err != nil {
		return 0, 0, fmt.Errorf("error reading footer: %v", err)
	}
	return parseFooter(footer[:])
}

// TOCEntry is an entry in the stargz file's TOC (Table of Contents).
type TOCEntry struct {
	// Name is the tar entry's name. It is the complete path
	// stored in the tar file, not just the base name.
	Name string `json:"name"`

	// Type is one of "dir", "reg", "symlink", "hardlink", "char",
	// "block", "fifo", or "chunk".
	// The "chunk" type is used for regular file data chunks past the first
	// TOCEntry; the 2nd chunk and on have only Type ("chunk"), Offset,
	// ChunkOffset, and ChunkSize populated.
	Type string `json:"type"`

	// Size, for regular files, is the logical size of the file.
	Size int64 `json:"size,omitempty"`

	// ModTime3339 is the modification time of the tar entry. Empty
	// means zero or unknown. Otherwise it's in UTC RFC3339
	// format. Use the ModTime method to access the time.Time value.
	ModTime3339 string `json:"modtime,omitempty"`
	modTime     time.Time

	// LinkName, for symlinks and hardlinks, is the link target.
	LinkName string `json:"linkName,omitempty"`

	// Mode is the permission and mode bits.
	Mode int64 `json:"mode,omitempty"`

	// UID is the user ID of the owner.
	UID int `json:"uid,omitempty"`

	// GID is the group ID of the owner.
	GID int `json:"gid,omitempty"`

	// Uname is the username of the owner.
	//
	// In the serialized JSON, this field may only be present for
	// the first entry with the same UID.
	Uname string `json:"userName,omitempty"`

	// Gname is the group name of the owner.
	//
	// In the serialized JSON, this field may only be present for
	// the first entry with the same GID.
	Gname string `json:"groupName,omitempty"`

	// Offset, for regular files, provides the offset in the
	// stargz file to the file's data bytes. See ChunkOffset and
	// ChunkSize.
	Offset int64 `json:"offset,omitempty"`

	nextOffset int64 // the Offset of the next entry with a non-zero Offset

	// DevMajor is the major device number for "char" and "block" types.
	DevMajor int `json:"devMajor,omitempty"`

	// DevMinor is the major device number for "char" and "block" types.
	DevMinor int `json:"devMinor,omitempty"`

	// NumLink is the number of entry names pointing to this entry.
	// Zero means one name references this entry.
	NumLink int

	// Xattrs are the extended attribute for the entry.
	Xattrs map[string][]byte `json:"xattrs,omitempty"`

	// Digest stores the OCI checksum for regular files payload.
	// It has the form "sha256:abcdef01234....".
	Digest string `json:"digest,omitempty"`

	// ChunkOffset is non-zero if this is a chunk of a large,
	// regular file. If so, the Offset is where the gzip header of
	// ChunkSize bytes at ChunkOffset in Name begin.
	//
	// In serialized form, a "chunkSize" JSON field of zero means
	// that the chunk goes to the end of the file. After reading
	// from the stargz TOC, though, the ChunkSize is initialized
	// to a non-zero file for when Type is either "reg" or
	// "chunk".
	ChunkOffset int64 `json:"chunkOffset,omitempty"`
	ChunkSize   int64 `json:"chunkSize,omitempty"`

	children map[string]*TOCEntry
}

// ModTime returns the entry's modification time.
func (e *TOCEntry) ModTime() time.Time { return e.modTime }

// NextOffset returns the position (relative to the start of the
// stargz file) of the next gzip boundary after e.Offset.
func (e *TOCEntry) NextOffset() int64 { return e.nextOffset }

func (e *TOCEntry) addChild(baseName string, child *TOCEntry) {
	if e.children == nil {
		e.children = make(map[string]*TOCEntry)
	}
	if child.Type == "dir" {
		e.NumLink++ // Entry ".." in the subdirectory links to this directory
	}
	e.children[baseName] = child
}

// isDataType reports whether TOCEntry is a regular file or chunk (something that
// contains regular file data).
func (e *TOCEntry) isDataType() bool { return e.Type == "reg" || e.Type == "chunk" }

// jtoc is the JSON-serialized table of contents index of the files in the stargz file.
type jtoc struct {
	Version int         `json:"version"`
	Entries []*TOCEntry `json:"entries"`
}

// Stat returns a FileInfo value representing e.
func (e *TOCEntry) Stat() os.FileInfo { return fileInfo{e} }

// ForeachChild calls f for each child item. If f returns false, iteration ends.
// If e is not a directory, f is not called.
func (e *TOCEntry) ForeachChild(f func(baseName string, ent *TOCEntry) bool) {
	for name, ent := range e.children {
		if !f(name, ent) {
			return
		}
	}
}

// LookupChild returns the directory e's child by its base name.
func (e *TOCEntry) LookupChild(baseName string) (child *TOCEntry, ok bool) {
	child, ok = e.children[baseName]
	return
}

// fileInfo implements os.FileInfo using the wrapped *TOCEntry.
type fileInfo struct{ e *TOCEntry }

var _ os.FileInfo = fileInfo{}

func (fi fileInfo) Name() string       { return path.Base(fi.e.Name) }
func (fi fileInfo) IsDir() bool        { return fi.e.Type == "dir" }
func (fi fileInfo) Size() int64        { return fi.e.Size }
func (fi fileInfo) ModTime() time.Time { return fi.e.ModTime() }
func (fi fileInfo) Sys() interface{}   { return fi.e }
func (fi fileInfo) Mode() (m os.FileMode) {
	m = os.FileMode(fi.e.Mode) & os.ModePerm
	switch fi.e.Type {
	case "dir":
		m |= os.ModeDir
	case "symlink":
		m |= os.ModeSymlink
	case "char":
		m |= os.ModeDevice | os.ModeCharDevice
	case "block":
		m |= os.ModeDevice
	case "fifo":
		m |= os.ModeNamedPipe
	}
	// TODO: ModeSetuid, ModeSetgid, if/as needed.
	return m
}

// initFields populates the Reader from r.toc after decoding it from
// JSON.
//
// Unexported fields are populated and TOCEntry fields that were
// implicit in the JSON are populated.
func (r *Reader) initFields() error {
	r.m = make(map[string]*TOCEntry, len(r.toc.Entries))
	r.chunks = make(map[string][]*TOCEntry)
	var lastPath string
	uname := map[int]string{}
	gname := map[int]string{}
	var lastRegEnt *TOCEntry
	for _, ent := range r.toc.Entries {
		ent.Name = strings.TrimPrefix(ent.Name, "./")
		if ent.Type == "reg" {
			lastRegEnt = ent
		}
		if ent.Type == "chunk" {
			ent.Name = lastPath
			r.chunks[ent.Name] = append(r.chunks[ent.Name], ent)
			if ent.ChunkSize == 0 && lastRegEnt != nil {
				ent.ChunkSize = lastRegEnt.Size - ent.ChunkOffset
			}
		} else {
			lastPath = ent.Name

			if ent.Uname != "" {
				uname[ent.UID] = ent.Uname
			} else {
				ent.Uname = uname[ent.UID]
			}
			if ent.Gname != "" {
				gname[ent.GID] = ent.Gname
			} else {
				ent.Gname = uname[ent.GID]
			}

			ent.modTime, _ = time.Parse(time.RFC3339, ent.ModTime3339)

			if ent.Type == "dir" {
				ent.NumLink++ // Parent dir links to this directory
				r.m[strings.TrimSuffix(ent.Name, "/")] = ent
			} else {
				r.m[ent.Name] = ent
			}
		}
		if ent.Type == "reg" && ent.ChunkSize > 0 && ent.ChunkSize < ent.Size {
			r.chunks[ent.Name] = make([]*TOCEntry, 0, ent.Size/ent.ChunkSize+1)
			r.chunks[ent.Name] = append(r.chunks[ent.Name], ent)
		}
		if ent.ChunkSize == 0 && ent.Size != 0 {
			ent.ChunkSize = ent.Size
		}
	}

	// Populate children, add implicit directories:
	for _, ent := range r.toc.Entries {
		if ent.Type == "chunk" {
			continue
		}
		// add "foo/":
		//    add "foo" child to "" (creating "" if necessary)
		//
		// add "foo/bar/":
		//    add "bar" child to "foo" (creating "foo" if necessary)
		//
		// add "foo/bar.txt":
		//    add "bar.txt" child to "foo" (creating "foo" if necessary)
		//
		// add "a/b/c/d/e/f.txt":
		//    create "a/b/c/d/e" node
		//    add "f.txt" child to "e"

		name := ent.Name
		if ent.Type == "dir" {
			name = strings.TrimSuffix(name, "/")
		}
		pdir := r.getOrCreateDir(parentDir(name))
		ent.NumLink++ // at least one name(ent.Name) references this entry.
		if ent.Type == "hardlink" {
			if org, ok := r.m[ent.LinkName]; ok {
				org.NumLink++ // original entry is referenced by this ent.Name.
				ent = org
			} else {
				return fmt.Errorf("%q is a hardlink but the linkname %q isn't found", ent.Name, ent.LinkName)
			}
		}
		pdir.addChild(path.Base(name), ent)
	}

	lastOffset := r.sr.Size()
	for i := len(r.toc.Entries) - 1; i >= 0; i-- {
		e := r.toc.Entries[i]
		if e.isDataType() {
			e.nextOffset = lastOffset
		}
		if e.Offset != 0 {
			lastOffset = e.Offset
		}
	}

	return nil
}

func parentDir(p string) string {
	dir, _ := path.Split(p)
	return strings.TrimSuffix(dir, "/")
}

func (r *Reader) getOrCreateDir(d string) *TOCEntry {
	e, ok := r.m[d]
	if !ok {
		e = &TOCEntry{
			Name:    d,
			Type:    "dir",
			Mode:    0755,
			NumLink: 2, // The directory itself(.) and the parent link to this directory.
		}
		r.m[d] = e
		if d != "" {
			pdir := r.getOrCreateDir(parentDir(d))
			pdir.addChild(path.Base(d), e)
		}
	}
	return e
}

// ChunkEntryForOffset returns the TOCEntry containing the byte of the
// named file at the given offset within the file.
func (r *Reader) ChunkEntryForOffset(name string, offset int64) (e *TOCEntry, ok bool) {
	e, ok = r.Lookup(name)
	if !ok || !e.isDataType() {
		return nil, false
	}
	ents := r.chunks[name]
	if len(ents) < 2 {
		if offset >= e.ChunkSize {
			return nil, false
		}
		return e, true
	}
	i := sort.Search(len(ents), func(i int) bool {
		e := ents[i]
		return e.ChunkOffset >= offset || (offset > e.ChunkOffset && offset < e.ChunkOffset+e.ChunkSize)
	})
	if i == len(ents) {
		return nil, false
	}
	return ents[i], true
}

// Lookup returns the Table of Contents entry for the given path.
//
// To get the root directory, use the empty string.
func (r *Reader) Lookup(path string) (e *TOCEntry, ok bool) {
	if r == nil {
		return
	}
	e, ok = r.m[path]
	if ok && e.Type == "hardlink" {
		e, ok = r.m[e.LinkName]
	}
	return
}

func (r *Reader) OpenFile(name string) (*io.SectionReader, error) {
	ent, ok := r.Lookup(name)
	if !ok {
		// TODO: come up with some error plan. This is lazy:
		return nil, &os.PathError{
			Path: name,
			Op:   "OpenFile",
			Err:  os.ErrNotExist,
		}
	}
	if ent.Type != "reg" {
		return nil, &os.PathError{
			Path: name,
			Op:   "OpenFile",
			Err:  errors.New("not a regular file"),
		}
	}
	fr := &fileReader{
		r:    r,
		size: ent.Size,
		ents: r.getChunks(ent),
	}
	return io.NewSectionReader(fr, 0, fr.size), nil
}

func (r *Reader) getChunks(ent *TOCEntry) []*TOCEntry {
	if ents, ok := r.chunks[ent.Name]; ok {
		return ents
	}
	return []*TOCEntry{ent}
}

type fileReader struct {
	r    *Reader
	size int64
	ents []*TOCEntry // 1 or more reg/chunk entries
}

func (fr *fileReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= fr.size {
		return 0, io.EOF
	}
	if off < 0 {
		return 0, errors.New("invalid offset")
	}
	var i int
	if len(fr.ents) > 1 {
		i = sort.Search(len(fr.ents), func(i int) bool {
			return fr.ents[i].ChunkOffset >= off
		})
		if i == len(fr.ents) {
			i = len(fr.ents) - 1
		}
	}
	ent := fr.ents[i]
	if ent.ChunkOffset > off {
		if i == 0 {
			return 0, errors.New("internal error; first chunk offset is non-zero")
		}
		ent = fr.ents[i-1]
	}

	//  If ent is a chunk of a large file, adjust the ReadAt
	//  offset by the chunk's offset.
	off -= ent.ChunkOffset

	finalEnt := fr.ents[len(fr.ents)-1]
	gzOff := ent.Offset
	// gzBytesRemain is the number of compressed gzip bytes in this
	// file remaining, over 1+ gzip chunks.
	gzBytesRemain := finalEnt.NextOffset() - gzOff

	sr := io.NewSectionReader(fr.r.sr, gzOff, gzBytesRemain)

	const maxGZread = 2 << 20
	var bufSize = maxGZread
	if gzBytesRemain < maxGZread {
		bufSize = int(gzBytesRemain)
	}

	br := bufio.NewReaderSize(sr, bufSize)
	if _, err := br.Peek(bufSize); err != nil {
		return 0, fmt.Errorf("fileReader.ReadAt.peek: %v", err)
	}

	gz, err := gzip.NewReader(br)
	if err != nil {
		return 0, fmt.Errorf("fileReader.ReadAt.gzipNewReader: %v", err)
	}
	if n, err := io.CopyN(ioutil.Discard, gz, off); n != off || err != nil {
		return 0, fmt.Errorf("discard of %d bytes = %v, %v", off, n, err)
	}
	return io.ReadFull(gz, p)
}

// A Writer writes stargz files.
//
// Use NewWriter to create a new Writer.
type Writer struct {
	bw       *bufio.Writer
	cw       *countWriter
	toc      *jtoc
	diffHash hash.Hash // SHA-256 of uncompressed tar

	closed        bool
	gz            *gzip.Writer
	lastUsername  map[int]string
	lastGroupname map[int]string

	// ChunkSize optionally controls the maximum number of bytes
	// of data of a regular file that can be written in one gzip
	// stream before a new gzip stream is started.
	// Zero means to use a default, currently 4 MiB.
	ChunkSize int
}

// currentGzipWriter writes to the current w.gz field, which can
// change throughout writing a tar entry.
//
// Additionally, it updates w's SHA-256 of the uncompressed bytes
// of the tar file.
type currentGzipWriter struct{ w *Writer }

func (cgw currentGzipWriter) Write(p []byte) (int, error) {
	cgw.w.diffHash.Write(p)
	return cgw.w.gz.Write(p)
}

func (w *Writer) chunkSize() int {
	if w.ChunkSize <= 0 {
		return 4 << 20
	}
	return w.ChunkSize
}

// NewWriter returns a new stargz writer writing to w.
//
// The writer must be closed to write its trailing table of contents.
func NewWriter(w io.Writer) *Writer {
	bw := bufio.NewWriter(w)
	cw := &countWriter{w: bw}
	return &Writer{
		bw:       bw,
		cw:       cw,
		toc:      &jtoc{Version: 1},
		diffHash: sha256.New(),
	}
}

// Close writes the stargz's table of contents and flushes all the
// buffers, returning any error.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	defer func() { w.closed = true }()

	if err := w.closeGz(); err != nil {
		return err
	}

	// Write the TOC index.
	tocOff := w.cw.n
	w.gz, _ = gzip.NewWriterLevel(w.cw, gzip.BestCompression)
	tw := tar.NewWriter(currentGzipWriter{w})
	tocJSON, err := json.MarshalIndent(w.toc, "", "\t")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     TOCTarName,
		Size:     int64(len(tocJSON)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(tocJSON); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := w.closeGz(); err != nil {
		return err
	}

	// And a little footer with pointer to the TOC gzip stream.
	if _, err := w.bw.Write(FooterBytes(tocOff)); err != nil {
		return err
	}

	if err := w.bw.Flush(); err != nil {
		return err
	}

	return nil
}

func (w *Writer) closeGz() error {
	if w.closed {
		return errors.New("write on closed Writer")
	}
	if w.gz != nil {
		if err := w.gz.Close(); err != nil {
			return err
		}
		w.gz = nil
	}
	return nil
}

// nameIfChanged returns name, unless it was the already the value of (*mp)[id],
// in which case it returns the empty string.
func (w *Writer) nameIfChanged(mp *map[int]string, id int, name string) string {
	if name == "" {
		return ""
	}
	if *mp == nil {
		*mp = make(map[int]string)
	}
	if (*mp)[id] == name {
		return ""
	}
	(*mp)[id] = name
	return name
}

func (w *Writer) condOpenGz() {
	if w.gz == nil {
		w.gz, _ = gzip.NewWriterLevel(w.cw, gzip.BestCompression)
	}
}

// AppendTar reads the tar or tar.gz file from r and appends
// each of its contents to w.
//
// The input r can optionally be gzip compressed but the output will
// always be gzip compressed.
func (w *Writer) AppendTar(r io.Reader) error {
	br := bufio.NewReader(r)
	var tr *tar.Reader
	if isGzip(br) {
		// NewReader can't fail if isGzip returned true.
		zr, _ := gzip.NewReader(br)
		tr = tar.NewReader(zr)
	} else {
		tr = tar.NewReader(br)
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading from source tar: tar.Reader.Next: %v", err)
		}
		if h.Name == TOCTarName {
			// It is possible for a layer to be "stargzified" twice during the
			// distribution lifecycle. So we reserve "TOCTarName" here to avoid
			// duplicated entries in the resulting layer.
			continue
		}

		xattrs := make(map[string][]byte)
		const xattrPAXRecordsPrefix = "SCHILY.xattr."
		if h.PAXRecords != nil {
			for k, v := range h.PAXRecords {
				if strings.HasPrefix(k, xattrPAXRecordsPrefix) {
					xattrs[k[len(xattrPAXRecordsPrefix):]] = []byte(v)
				}
			}
		}
		ent := &TOCEntry{
			Name:        h.Name,
			Mode:        h.Mode,
			UID:         h.Uid,
			GID:         h.Gid,
			Uname:       w.nameIfChanged(&w.lastUsername, h.Uid, h.Uname),
			Gname:       w.nameIfChanged(&w.lastGroupname, h.Gid, h.Gname),
			ModTime3339: formatModtime(h.ModTime),
			Xattrs:      xattrs,
		}
		w.condOpenGz()
		tw := tar.NewWriter(currentGzipWriter{w})
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeLink:
			ent.Type = "hardlink"
			ent.LinkName = h.Linkname
		case tar.TypeSymlink:
			ent.Type = "symlink"
			ent.LinkName = h.Linkname
		case tar.TypeDir:
			ent.Type = "dir"
		case tar.TypeReg:
			ent.Type = "reg"
			ent.Size = h.Size
		case tar.TypeChar:
			ent.Type = "char"
			ent.DevMajor = int(h.Devmajor)
			ent.DevMinor = int(h.Devminor)
		case tar.TypeBlock:
			ent.Type = "block"
			ent.DevMajor = int(h.Devmajor)
			ent.DevMinor = int(h.Devminor)
		case tar.TypeFifo:
			ent.Type = "fifo"
		default:
			return fmt.Errorf("unsupported input tar entry %q", h.Typeflag)
		}

		// We need to keep a reference to the TOC entry for regular files, so that we
		// can fill the digest later.
		var regFileEntry *TOCEntry
		var payloadDigest hash.Hash
		if h.Typeflag == tar.TypeReg {
			regFileEntry = ent
			payloadDigest = sha256.New()
		}

		if h.Typeflag == tar.TypeReg && ent.Size > 0 {
			var written int64
			totalSize := ent.Size // save it before we destroy ent
			tee := io.TeeReader(tr, payloadDigest)
			for written < totalSize {
				if err := w.closeGz(); err != nil {
					return err
				}

				chunkSize := int64(w.chunkSize())
				remain := totalSize - written
				if remain < chunkSize {
					chunkSize = remain
				} else {
					ent.ChunkSize = chunkSize
				}
				ent.Offset = w.cw.n
				ent.ChunkOffset = written

				w.condOpenGz()

				if _, err := io.CopyN(tw, tee, chunkSize); err != nil {
					return fmt.Errorf("error copying %q: %v", h.Name, err)
				}
				w.toc.Entries = append(w.toc.Entries, ent)
				written += chunkSize
				ent = &TOCEntry{
					Name: h.Name,
					Type: "chunk",
				}
			}
		} else {
			w.toc.Entries = append(w.toc.Entries, ent)
		}
		if payloadDigest != nil {
			regFileEntry.Digest = fmt.Sprintf("sha256:%x", payloadDigest.Sum(nil))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// DiffID returns the SHA-256 of the uncompressed tar bytes.
// It is only valid to call DiffID after Close.
func (w *Writer) DiffID() string {
	return fmt.Sprintf("sha256:%x", w.diffHash.Sum(nil))
}

// FooterBytes returns the 51 bytes footer.
func FooterBytes(tocOff int64) []byte {
	buf := bytes.NewBuffer(make([]byte, 0, FooterSize))
	gz, _ := gzip.NewWriterLevel(buf, gzip.NoCompression)

	// Extra header indicating the offset of TOCJSON
	// https://tools.ietf.org/html/rfc1952#section-2.3.1.1
	header := make([]byte, 4)
	header[0], header[1] = 'S', 'G'
	subfield := fmt.Sprintf("%016xSTARGZ", tocOff)
	binary.LittleEndian.PutUint16(header[2:4], uint16(len(subfield))) // little-endian per RFC1952
	gz.Header.Extra = append(header, []byte(subfield)...)
	gz.Close()
	if buf.Len() != FooterSize {
		panic(fmt.Sprintf("footer buffer = %d, not %d", buf.Len(), FooterSize))
	}
	return buf.Bytes()
}

func parseFooter(p []byte) (tocOffset int64, footerSize int64, rErr error) {
	tocOffset, err := parseEStargzFooter(p)
	if err == nil {
		return tocOffset, FooterSize, nil
	}
	rErr = multierror.Append(rErr, err)

	pad := len(p) - legacyFooterSize
	if pad < 0 {
		pad = 0
	}
	tocOffset, err = parseLegacyFooter(p[pad:])
	if err == nil {
		return tocOffset, legacyFooterSize, nil
	}
	rErr = multierror.Append(rErr, err)
	return
}

func parseEStargzFooter(p []byte) (tocOffset int64, err error) {
	if len(p) != FooterSize {
		return 0, fmt.Errorf("invalid length %d cannot be parsed", len(p))
	}
	zr, err := gzip.NewReader(bytes.NewReader(p))
	if err != nil {
		return 0, err
	}
	extra := zr.Header.Extra
	si1, si2, subfieldlen, subfield := extra[0], extra[1], extra[2:4], extra[4:]
	if si1 != 'S' || si2 != 'G' {
		return 0, fmt.Errorf("invalid subfield IDs: %q, %q; want E, S", si1, si2)
	}
	if slen := binary.LittleEndian.Uint16(subfieldlen); slen != uint16(16+len("STARGZ")) {
		return 0, fmt.Errorf("invalid length of subfield %d; want %d", slen, 16+len("STARGZ"))
	}
	if string(subfield[16:]) != "STARGZ" {
		return 0, fmt.Errorf("STARGZ magic string must be included in the footer subfield")
	}
	return strconv.ParseInt(string(subfield[:16]), 16, 64)
}

func parseLegacyFooter(p []byte) (tocOffset int64, err error) {
	if len(p) != legacyFooterSize {
		return 0, fmt.Errorf("legacy: invalid length %d cannot be parsed", len(p))
	}
	zr, err := gzip.NewReader(bytes.NewReader(p))
	if err != nil {
		return 0, errors.Wrapf(err, "legacy: failed to get footer gzip reader")
	}
	extra := zr.Header.Extra
	if len(extra) != 16+len("STARGZ") {
		return 0, fmt.Errorf("legacy: invalid stargz's extra field size")
	}
	if string(extra[16:]) != "STARGZ" {
		return 0, fmt.Errorf("legacy: magic string STARGZ not found")
	}
	return strconv.ParseInt(string(extra[:16]), 16, 64)
}

func formatModtime(t time.Time) string {
	if t.IsZero() || t.Unix() == 0 {
		return ""
	}
	return t.UTC().Round(time.Second).Format(time.RFC3339)
}

// countWriter counts how many bytes have been written to its wrapped
// io.Writer.
type countWriter struct {
	w io.Writer
	n int64
}

func (cw *countWriter) Write(p []byte) (n int, err error) {
	n, err = cw.w.Write(p)
	cw.n += int64(n)
	return
}

// isGzip reports whether br is positioned right before an upcoming gzip stream.
// It does not consume any bytes from br.
func isGzip(br *bufio.Reader) bool {
	const (
		gzipID1     = 0x1f
		gzipID2     = 0x8b
		gzipDeflate = 8
	)
	peek, _ := br.Peek(3)
	return len(peek) >= 3 && peek[0] == gzipID1 && peek[1] == gzipID2 && peek[2] == gzipDeflate
}
