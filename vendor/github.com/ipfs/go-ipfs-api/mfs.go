package shell

import (
	"context"
	"io"

	files "github.com/ipfs/go-ipfs-files"
)

type FilesOpt func(*RequestBuilder) error

type MfsLsEntry struct {
	Name string
	Type uint8
	Size uint64
	Hash string
}

type FilesStatObject struct {
	Blocks         int
	CumulativeSize uint64
	Hash           string
	Local          bool
	Size           uint64
	SizeLocal      uint64
	Type           string
	WithLocality   bool
}

type filesLsOutput struct {
	Entries []*MfsLsEntry
}

type filesFlushOutput struct {
	Cid string
}

type filesLs struct{}
type filesChcid struct{}
type filesMkdir struct{}
type filesRead struct{}
type filesWrite struct{}
type filesStat struct{}

var (
	FilesLs    filesLs
	FilesChcid filesChcid
	FilesMkdir filesMkdir
	FilesRead  filesRead
	FilesWrite filesWrite
	FilesStat  filesStat
)

// Stat use long listing format
func (filesLs) Stat(long bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("long", long)
		return nil
	}
}

// CidVersion cid version to use. (experimental)
func (filesChcid) CidVersion(version int) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("cid-version", version)
		return nil
	}
}

// Hash hash function to use. Will set Cid version to 1 if used. (experimental)
func (filesChcid) Hash(hash string) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("hash", hash)
		return nil
	}
}

// Parents no error if existing, make parent directories as needed
func (filesMkdir) Parents(parents bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("parents", parents)
		return nil
	}
}

// CidVersion cid version to use. (experimental)
func (filesMkdir) CidVersion(version int) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("cid-version", version)
		return nil
	}
}

// Hash hash function to use. Will set Cid version to 1 if used. (experimental)
func (filesMkdir) Hash(hash string) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("hash", hash)
		return nil
	}
}

// Offset byte offset to begin reading from
func (filesRead) Offset(offset int64) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("offset", offset)
		return nil
	}
}

// Count maximum number of bytes to read
func (filesRead) Count(count int64) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("count", count)
		return nil
	}
}

// Hash print only hash. Implies '--format=<hash>'. Conflicts with other format options.
func (filesStat) Hash(hash bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("hash", hash)
		return nil
	}
}

// Size print only size. Implies '--format=<cumulsize>'. Conflicts with other format options.
func (filesStat) Size(size bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("size", size)
		return nil
	}
}

// WithLocal compute the amount of the dag that is local, and if possible the total size.
func (filesStat) WithLocal(withLocal bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("with-local", withLocal)
		return nil
	}
}

// Offset byte offset to begin writing at
func (filesWrite) Offset(offset int64) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("offset", offset)
		return nil
	}
}

// Create create the file if it does not exist
func (filesWrite) Create(create bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("create", create)
		return nil
	}
}

// Parents make parent directories as needed
func (filesWrite) Parents(parents bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("parents", parents)
		return nil
	}
}

// Truncate truncate the file to size zero before writing
func (filesWrite) Truncate(truncate bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("truncate", truncate)
		return nil
	}
}

// Count maximum number of bytes to write
func (filesWrite) Count(count int64) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("count", count)
		return nil
	}
}

// RawLeaves use raw blocks for newly created leaf nodes. (experimental)
func (filesWrite) RawLeaves(rawLeaves bool) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("raw-leaves", rawLeaves)
		return nil
	}
}

// CidVersion cid version to use. (experimental)
func (filesWrite) CidVersion(version int) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("cid-version", version)
		return nil
	}
}

// Hash hash function to use. Will set Cid version to 1 if used. (experimental)
func (filesWrite) Hash(hash string) FilesOpt {
	return func(rb *RequestBuilder) error {
		rb.Option("hash", hash)
		return nil
	}
}

// FilesChcid change the cid version or hash function of the root node of a given path
func (s *Shell) FilesChcid(ctx context.Context, path string, options ...FilesOpt) error {
	if len(path) == 0 {
		path = "/"
	}

	rb := s.Request("files/chcid", path)
	for _, opt := range options {
		if err := opt(rb); err != nil {
			return err
		}
	}

	return rb.Exec(ctx, nil)
}

// FilesCp copy any IPFS files and directories into MFS (or copy within MFS)
func (s *Shell) FilesCp(ctx context.Context, src string, dest string) error {
	return s.Request("files/cp", src, dest).Exec(ctx, nil)
}

// FilesFlush flush a given path's data to disk
func (s *Shell) FilesFlush(ctx context.Context, path string) (string, error) {
	if len(path) == 0 {
		path = "/"
	}
	out := &filesFlushOutput{}
	if err := s.Request("files/flush", path).
		Exec(ctx, out); err != nil {
		return "", err
	}

	return out.Cid, nil
}

// FilesLs list directories in the local mutable namespace
func (s *Shell) FilesLs(ctx context.Context, path string, options ...FilesOpt) ([]*MfsLsEntry, error) {
	if len(path) == 0 {
		path = "/"
	}

	var out filesLsOutput
	rb := s.Request("files/ls", path)
	for _, opt := range options {
		if err := opt(rb); err != nil {
			return nil, err
		}
	}
	if err := rb.Exec(ctx, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// FilesMkdir make directories
func (s *Shell) FilesMkdir(ctx context.Context, path string, options ...FilesOpt) error {
	rb := s.Request("files/mkdir", path)
	for _, opt := range options {
		if err := opt(rb); err != nil {
			return err
		}
	}

	return rb.Exec(ctx, nil)
}

// FilesMv move files
func (s *Shell) FilesMv(ctx context.Context, src string, dest string) error {
	return s.Request("files/mv", src, dest).Exec(ctx, nil)
}

// FilesRead read a file in a given MFS
func (s *Shell) FilesRead(ctx context.Context, path string, options ...FilesOpt) (io.ReadCloser, error) {
	rb := s.Request("files/read", path)
	for _, opt := range options {
		if err := opt(rb); err != nil {
			return nil, err
		}
	}

	resp, err := rb.Send(ctx)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}

	return resp.Output, nil
}

// FilesRm remove a file
func (s *Shell) FilesRm(ctx context.Context, path string, force bool) error {
	return s.Request("files/rm", path).
		Option("force", force).
		Exec(ctx, nil)
}

// FilesStat display file status
func (s *Shell) FilesStat(ctx context.Context, path string, options ...FilesOpt) (*FilesStatObject, error) {
	out := &FilesStatObject{}

	rb := s.Request("files/stat", path)
	for _, opt := range options {
		if err := opt(rb); err != nil {
			return nil, err
		}
	}

	if err := rb.Exec(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// FilesWrite write to a mutable file in a given filesystem
func (s *Shell) FilesWrite(ctx context.Context, path string, data io.Reader, options ...FilesOpt) error {
	fr := files.NewReaderFile(data)
	slf := files.NewSliceDirectory([]files.DirEntry{files.FileEntry("", fr)})
	fileReader := files.NewMultiFileReader(slf, true)

	rb := s.Request("files/write", path)
	for _, opt := range options {
		if err := opt(rb); err != nil {
			return err
		}
	}

	return rb.Body(fileReader).Exec(ctx, nil)
}
