package winlayers

import (
	"archive/tar"
	"context"
	"io"
	"strings"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/archive"
	"github.com/containerd/containerd/v2/pkg/archive/compression"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/util/bklog"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func NewFileSystemApplierWithWindows(cs content.Provider, a diff.Applier) diff.Applier {
	return &winApplier{
		cs: cs,
		a:  a,
	}
}

type winApplier struct {
	cs content.Provider
	a  diff.Applier
}

func (s *winApplier) Apply(ctx context.Context, desc ocispecs.Descriptor, mounts []mount.Mount, opts ...diff.ApplyOpt) (d ocispecs.Descriptor, err error) {
	// HACK:, containerd doesn't know about vnd.docker.image.rootfs.diff.tar.zstd, but that
	// media type is compatible w/ the oci type, so just lie and say it's the oci type
	if desc.MediaType == images.MediaTypeDockerSchema2LayerZstd {
		desc.MediaType = ocispecs.MediaTypeImageLayerZstd
	}

	if hasWindowsLayerMode(ctx) {
		return s.applyWindowsLayer(ctx, desc, mounts, opts...)
	}
	if hasLinuxLayerMode(ctx) {
		return s.applyLinuxLayer(ctx, desc, mounts, opts...)
	}
	return s.apply(ctx, desc, mounts, opts...)
}

// applyLayerBlob reads desc from the content store (decompressing as needed),
// tees it through a digester+counter, runs fn for the OS-specific extraction,
// drains any trailing bytes, and returns the resulting layer descriptor. It is
// the shared blob-handling core of applyWindowsLayer and applyLinuxLayer.
func (s *winApplier) applyLayerBlob(ctx context.Context, desc ocispecs.Descriptor, fn func(rc *readCounter) error) (ocispecs.Descriptor, error) {
	compressed, err := images.DiffCompression(ctx, desc.MediaType)
	if err != nil {
		return ocispecs.Descriptor{}, errors.Wrapf(cerrdefs.ErrNotImplemented, "unsupported diff media type: %v", desc.MediaType)
	}

	ra, err := s.cs.ReaderAt(ctx, desc)
	if err != nil {
		return ocispecs.Descriptor{}, errors.Wrap(err, "failed to get reader from content store")
	}
	defer ra.Close()

	r := content.NewReader(ra)
	if compressed != "" {
		ds, err := compression.DecompressStream(r)
		if err != nil {
			return ocispecs.Descriptor{}, err
		}
		defer ds.Close()
		r = ds
	}

	digester := digest.Canonical.Digester()
	rc := &readCounter{
		r: io.TeeReader(r, digester.Hash()),
	}

	if err := fn(rc); err != nil {
		return ocispecs.Descriptor{}, err
	}

	// Read any trailing data so the size/digest cover the whole blob.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return ocispecs.Descriptor{}, err
	}

	return ocispecs.Descriptor{
		MediaType: ocispecs.MediaTypeImageLayer,
		Size:      rc.c,
		Digest:    digester.Digest(),
	}, nil
}

// applyWindowsLayer applies a Windows-format layer on a Linux host by stripping
// the "Files/" prefix from tar entries. opts is unused: the per-OS handlers do
// the work themselves rather than forwarding to a sub-applier.
func (s *winApplier) applyWindowsLayer(ctx context.Context, desc ocispecs.Descriptor, mounts []mount.Mount, _ ...diff.ApplyOpt) (d ocispecs.Descriptor, err error) {
	return s.applyLayerBlob(ctx, desc, func(rc *readCounter) error {
		return mount.WithTempMount(ctx, mounts, func(root string) error {
			rc2, discard := filter(rc, func(hdr *tar.Header) bool {
				if after, ok := strings.CutPrefix(hdr.Name, "Files/"); ok {
					hdr.Name = after
					hdr.Linkname = strings.TrimPrefix(hdr.Linkname, "Files/")
					// TODO: could convert the windows PAX headers to xattr here to reuse
					// the original ones in diff for parent directories and file modifications
					return true
				}
				return false
			})

			if _, err := archive.Apply(ctx, root, rc2); err != nil {
				discard(err)
				return err
			}
			return nil
		})
	})
}

// applyLinuxLayer applies a Linux-format layer on a Windows host by wrapping
// the tar into Windows layer structure and importing it via HCS. See
// applyWindowsLayer for why opts is unused.
func (s *winApplier) applyLinuxLayer(ctx context.Context, desc ocispecs.Descriptor, mounts []mount.Mount, _ ...diff.ApplyOpt) (d ocispecs.Descriptor, err error) {
	bklog.G(ctx).Infof("linuxlayer-apply: enter desc.digest=%s desc.size=%d media=%s mounts=%d", desc.Digest, desc.Size, desc.MediaType, len(mounts))
	for i, m := range mounts {
		bklog.G(ctx).Infof("linuxlayer-apply: mount[%d] type=%s source=%s target=%s opts=%v", i, m.Type, m.Source, m.Target, m.Options)
	}
	if len(mounts) == 0 {
		return ocispecs.Descriptor{}, errors.New("no mounts provided for linux layer apply")
	}

	snapshotDir := mounts[0].Source
	parentPaths := getParentLayerPaths(mounts)

	return s.applyLayerBlob(ctx, desc, func(rc *readCounter) error {
		// Wrap the Linux tar into Windows layer structure, then import via HCS.
		wrappedTar, discard, done := wrapLinuxToWindows(ctx, rc)
		if _, err := importLinuxLayerAsWindows(ctx, snapshotDir, wrappedTar, parentPaths); err != nil {
			discard(err)
			return errors.Wrap(err, "failed to import linux layer as windows layer")
		}
		if err := <-done; err != nil {
			return errors.Wrap(err, "failed wrapping linux tar")
		}
		return nil
	})
}

// wrapLinuxToWindows transforms a Linux-format tar stream into a Windows-format
// tar stream (see writeWrappedWindowsLayer). It returns a Reader the caller
// reads the Windows-format tar from.
func wrapLinuxToWindows(ctx context.Context, in io.Reader) (io.Reader, func(error), chan error) {
	pr, pw := io.Pipe()
	rc := &readCanceler{Reader: in}
	done := make(chan error, 1)

	go func() {
		err := writeWrappedWindowsLayer(rc, pw)
		if err != nil {
			bklog.G(ctx).Errorf("wrapLinuxToWindows %+v", err)
		}
		pw.CloseWithError(err)
		done <- err
	}()

	discard := func(err error) {
		rc.cancel(err)
		pw.CloseWithError(err)
	}

	return pr, discard, done
}

type readCounter struct {
	r io.Reader
	c int64
}

func (rc *readCounter) Read(p []byte) (n int, err error) {
	n, err = rc.r.Read(p)
	rc.c += int64(n)
	return
}

func filter(in io.Reader, f func(*tar.Header) bool) (io.Reader, func(error)) {
	pr, pw := io.Pipe()

	rc := &readCanceler{Reader: in}

	go func() {
		tarReader := tar.NewReader(rc)
		tarWriter := tar.NewWriter(pw)

		pw.CloseWithError(func() error {
			for {
				h, err := tarReader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				if f(h) {
					if err := tarWriter.WriteHeader(h); err != nil {
						return err
					}
					if h.Size > 0 {
						//nolint:gosec // never read into memory
						if _, err := io.Copy(tarWriter, tarReader); err != nil {
							return err
						}
					}
				} else if h.Size > 0 {
					//nolint:gosec // never read into memory
					if _, err := io.Copy(io.Discard, tarReader); err != nil {
						return err
					}
				}
			}
			return tarWriter.Close()
		}())
	}()

	discard := func(err error) {
		rc.cancel(err)
		pw.CloseWithError(err)
	}

	return pr, discard
}

type readCanceler struct {
	mu sync.Mutex
	io.Reader
	err error
}

func (r *readCanceler) Read(b []byte) (int, error) {
	r.mu.Lock()
	if r.err != nil {
		r.mu.Unlock()
		return 0, r.err
	}
	n, err := r.Reader.Read(b)
	r.mu.Unlock()
	return n, err
}

func (r *readCanceler) cancel(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}
