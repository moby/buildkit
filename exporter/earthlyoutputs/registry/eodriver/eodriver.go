package eodriver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"strings"
	"time"

	"github.com/containerd/containerd/content"
	storagedriver "github.com/docker/distribution/registry/storage/driver"
	"github.com/docker/distribution/registry/storage/driver/base"
	"github.com/docker/distribution/registry/storage/driver/factory"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	driverName        = "eodriver"
	defaultMaxThreads = uint64(100)

	// minThreads is the minimum value for the maxthreads configuration
	// parameter. If the driver's parameters are less than this we set
	// the parameters to minThreads
	minThreads = uint64(25)
)

// DriverParameters represents all configuration options available for the
// eodriver driver
type DriverParameters struct {
	RootDirectory string
	MaxThreads    uint64
}

func init() {
	factory.Register(driverName, &earthlyOutputDriverFactory{})
}

// earthlyOutputDriverFactory implements the factory.StorageDriverFactory interface
type earthlyOutputDriverFactory struct{}

func (factory *earthlyOutputDriverFactory) Create(parameters map[string]interface{}) (storagedriver.StorageDriver, error) {
	return FromParameters(parameters)
}

type driver struct {
	mmp *MultiMultiProvider
}

type baseEmbed struct {
	base.Base
}

// Driver is a storagedriver.StorageDriver implementation backed by a local
// a multi-multi-provider.
type Driver struct {
	baseEmbed
}

// FromParameters constructs a new Driver with a given parameters map
// Optional Parameters:
// - maxthreads
func FromParameters(parameters map[string]interface{}) (*Driver, error) {
	params, err := fromParametersImpl(parameters)
	if err != nil || params == nil {
		return nil, err
	}
	return New(*params), nil
}

func fromParametersImpl(parameters map[string]interface{}) (*DriverParameters, error) {
	var (
		err        error
		maxThreads = defaultMaxThreads
	)

	if parameters != nil {
		maxThreads, err = base.GetLimitFromParameter(parameters["maxthreads"], minThreads, defaultMaxThreads)
		if err != nil {
			return nil, fmt.Errorf("maxthreads config error: %s", err.Error())
		}
	}

	params := &DriverParameters{
		MaxThreads: maxThreads,
	}
	return params, nil
}

// New constructs a new Driver with a given rootDirectory
func New(params DriverParameters) *Driver {
	fsDriver := &driver{
		mmp: MultiMultiProviderSingleton,
	}

	return &Driver{
		baseEmbed: baseEmbed{
			Base: base.Base{
				StorageDriver: base.NewRegulator(fsDriver, params.MaxThreads),
			},
		},
	}
}

// Implement the storagedriver.StorageDriver interface

func (d *driver) Name() string {
	return driverName
}

// GetContent retrieves the content stored at "path" as a []byte.
func (d *driver) GetContent(ctx context.Context, path string) ([]byte, error) {
	rc, err := d.Reader(ctx, path, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	p, err := ioutil.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	return p, nil
}

// PutContent stores the []byte content at a location designated by "path".
func (d *driver) PutContent(ctx context.Context, subPath string, contents []byte) error {
	return storagedriver.ErrUnsupportedMethod{}
}

// Reader retrieves an io.ReadCloser for the content stored at "path" with a
// given byte offset.
func (d *driver) Reader(ctx context.Context, path string, offset int64) (io.ReadCloser, error) {
	rc, _, err := d.get(ctx, path, offset)
	return rc, err
}

func (d *driver) get(ctx context.Context, path string, offset int64) (io.ReadCloser, int64, error) {
	if !strings.HasPrefix(path, "/docker/registry/v2/") {
		return nil, 0, storagedriver.PathNotFoundError{}
	}
	subPath := strings.TrimPrefix(path, "/docker/registry/v2/")
	if strings.HasPrefix(subPath, "repositories/") {
		subSubPath := strings.TrimPrefix(subPath, "repositories/")
		subSubPathSplit := strings.Split(subSubPath, "/")
		indexPostImgName := -1
		for index, part := range subSubPathSplit {
			if strings.HasPrefix(part, "_") {
				indexPostImgName = index
				break
			}
		}
		if indexPostImgName == -1 {
			return nil, 0, storagedriver.PathNotFoundError{}
		}
		imgName := strings.Join(subSubPathSplit[0:indexPostImgName], "/")
		switch subSubPathSplit[indexPostImgName] {
		case "_manifests":
			switch subSubPathSplit[indexPostImgName+1] {
			case "tags":
				postTagsPath := strings.Join(subSubPathSplit[indexPostImgName+2:], "/")
				tag := strings.TrimSuffix(postTagsPath, "/current/link")
				fullImgName := fmt.Sprintf("%s:%s", imgName, tag)
				_, baseDigest, err := d.mmp.Get(ctx, fullImgName)
				if err != nil {
					return nil, 0, errors.Wrapf(err, "get %s", path)
				}
				return stringReadCloserOffset(baseDigest.String(), offset)
			case "revisions":
				postRevisionsPath := strings.Join(subSubPathSplit[indexPostImgName+2:], "/")
				if !strings.HasPrefix(postRevisionsPath, "sha256/") {
					return nil, 0, storagedriver.PathNotFoundError{}
				}
				sha := strings.TrimSuffix(strings.TrimPrefix(postRevisionsPath, "sha256/"), "/link")
				return stringReadCloserOffset(fmt.Sprintf("sha256:%s", sha), offset)
			default:
				return nil, 0, storagedriver.PathNotFoundError{}
			}
		case "_layers":
			postLayersPath := strings.Join(subSubPathSplit[indexPostImgName+1:], "/")
			if !strings.HasPrefix(postLayersPath, "sha256/") {
				return nil, 0, storagedriver.PathNotFoundError{}
			}
			sha := strings.TrimSuffix(strings.TrimPrefix(postLayersPath, "sha256/"), "/link")
			return stringReadCloserOffset(fmt.Sprintf("sha256:%s", sha), offset)
		default:
			return nil, 0, storagedriver.PathNotFoundError{}
		}
	} else if strings.HasPrefix(subPath, "blobs/sha256/") {
		subSubPath := strings.TrimPrefix(subPath, "blobs/sha256/")
		subSubPathSplit := strings.Split(subSubPath, "/")
		if len(subSubPathSplit) != 3 {
			return nil, 0, storagedriver.PathNotFoundError{}
		}
		if subSubPathSplit[2] != "data" {
			return nil, 0, storagedriver.PathNotFoundError{}
		}
		sha := subSubPathSplit[1]
		desc := ocispec.Descriptor{
			Digest: digest.Digest(fmt.Sprintf("sha256:%s", sha)),
		}
		ra, err := d.mmp.ReaderAt(ctx, desc)
		if err != nil {
			return nil, 0, errors.Wrapf(err, "blob %s", path)
		}
		return &readerAtReadCloser{
			ra:     ra,
			offset: offset,
		}, ra.Size(), nil
	} else {
		return nil, 0, storagedriver.PathNotFoundError{}
	}
}

func (d *driver) Writer(ctx context.Context, subPath string, append bool) (storagedriver.FileWriter, error) {
	return nil, storagedriver.ErrUnsupportedMethod{}
}

// Stat retrieves the FileInfo for the given path, including the current size
// in bytes and the creation time.
func (d *driver) Stat(ctx context.Context, subPath string) (storagedriver.FileInfo, error) {
	_, size, err := d.get(ctx, subPath, 0)
	if err != nil {
		return nil, err
	}

	return fileInfo{
		path:   subPath,
		size:   size,
		crTime: time.Now(),
	}, nil
}

// List returns a list of the objects that are direct descendants of the given
// path.
func (d *driver) List(ctx context.Context, subPath string) ([]string, error) {
	return nil, storagedriver.ErrUnsupportedMethod{}
}

// Move moves an object stored at sourcePath to destPath, removing the original
// object.
func (d *driver) Move(ctx context.Context, sourcePath string, destPath string) error {
	return storagedriver.ErrUnsupportedMethod{}
}

// Delete recursively deletes all objects stored at "path" and its subpaths.
func (d *driver) Delete(ctx context.Context, subPath string) error {
	return storagedriver.ErrUnsupportedMethod{}
}

// URLFor returns a URL which may be used to retrieve the content stored at the given path.
// May return an UnsupportedMethodErr in certain StorageDriver implementations.
func (d *driver) URLFor(ctx context.Context, path string, options map[string]interface{}) (string, error) {
	return "", storagedriver.ErrUnsupportedMethod{}
}

// Walk traverses a filesystem defined within driver, starting
// from the given path, calling f on each file
func (d *driver) Walk(ctx context.Context, path string, f storagedriver.WalkFn) error {
	return storagedriver.WalkFallback(ctx, d, path, f)
}

type fileInfo struct {
	size   int64
	path   string
	crTime time.Time
}

var _ storagedriver.FileInfo = fileInfo{}

// Path provides the full path of the target of this file info.
func (fi fileInfo) Path() string {
	return fi.path
}

// Size returns current length in bytes of the file. The return value can
// be used to write to the end of the file at path. The value is
// meaningless if IsDir returns true.
func (fi fileInfo) Size() int64 {
	return fi.size
}

// ModTime returns the modification time for the file. For backends that
// don't have a modification time, the creation time should be returned.
func (fi fileInfo) ModTime() time.Time {
	return fi.crTime
}

// IsDir returns true if the path is a directory.
func (fi fileInfo) IsDir() bool {
	return false
}

func stringReadCloserOffset(str string, offset int64) (io.ReadCloser, int64, error) {
	dt := []byte(str)
	if offset > int64(len(dt)) {
		return nil, 0, io.EOF
	}
	size := len(dt)
	dt = dt[offset:]
	reader := ioutil.NopCloser(bytes.NewReader(dt))
	return reader, int64(size), nil
}

type readerAtReadCloser struct {
	ra     content.ReaderAt
	offset int64
}

func (rarc *readerAtReadCloser) Read(p []byte) (int, error) {
	n, err := rarc.ra.ReadAt(p, rarc.offset)
	rarc.offset += int64(n)
	return n, err
}

func (rarc *readerAtReadCloser) Close() error {
	return rarc.ra.Close()
}
