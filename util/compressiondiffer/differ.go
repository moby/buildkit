package compressiondiffer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/mount"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"

	log "github.com/moby/buildkit/util/bklog"
)

func NewCompressionDiffer(store content.Store, d diff.Comparer) diff.Comparer {
	return &compressionDiffer{
		store: store,
		d:     d,
	}
}

const containerdUncompressed = "containerd.io/uncompressed"

var emptyDesc = ocispecs.Descriptor{}

// compressionDiffer computes diff respecting the passed compressor function
type compressionDiffer struct {
	store content.Store
	d     diff.Comparer
}

// Compare creates a diff between the given mounts and uploads the result
// to the content store.
func (d *compressionDiffer) Compare(ctx context.Context, lower, upper []mount.Mount, opts ...diff.Opt) (desc ocispecs.Descriptor, err error) {
	var config diff.Config
	for _, opt := range opts {
		if err := opt(&config); err != nil {
			return emptyDesc, err
		}
	}

	// If a custom compressor is specified, don't pass it to containerd.
	var compressorFunc func(dest io.Writer, mediaType string) (io.WriteCloser, error)
	var mediaType string
	var ref string
	if config.Compressor != nil {
		compressorFunc = config.Compressor
		config.Compressor = nil
		mediaType = config.MediaType
		config.MediaType = ocispecs.MediaTypeImageLayer
		ref = config.Reference
		config.Reference = uniqueRef()
	}
	desc, err = d.d.Compare(ctx, lower, upper, func(c *diff.Config) error {
		c.MediaType = config.MediaType
		c.Reference = config.Reference
		c.Labels = config.Labels
		c.Compressor = config.Compressor
		return nil
	})
	if err != nil {
		return emptyDesc, err
	}
	if compressorFunc == nil {
		// Return immeidately when no custom compressor is specified.
		return desc, err
	}

	// Fallback to the client-side compression because containerd differ doesn't support custom compressor.
	ra, err := d.store.ReaderAt(ctx, desc)
	if err != nil {
		return emptyDesc, err
	}
	defer ra.Close()
	cw, err := d.store.Writer(ctx,
		content.WithRef(ref),
		content.WithDescriptor(ocispecs.Descriptor{
			MediaType: mediaType, // most contentstore implementations just ignore this
		}))
	if err != nil {
		return emptyDesc, errors.Wrap(err, "failed to open writer")
	}

	// errOpen is set when an error occurs while the content writer has not been
	// committed or closed yet to force a cleanup
	var errOpen error
	defer func() {
		if errOpen != nil {
			cw.Close()
			if abortErr := d.store.Abort(ctx, ref); abortErr != nil {
				log.G(ctx).WithError(abortErr).WithField("ref", ref).Warnf("failed to delete diff upload")
			}
		}
	}()

	dgstr := digest.SHA256.Digester()
	var compressed io.WriteCloser
	bufW := bufio.NewWriter(cw)
	compressed, errOpen = compressorFunc(bufW, mediaType)
	if errOpen != nil {
		return emptyDesc, errors.Wrap(errOpen, "failed to get compressed stream")
	}
	if _, errOpen = io.Copy(io.MultiWriter(compressed, dgstr.Hash()), io.NewSectionReader(ra, 0, ra.Size())); errOpen != nil {
		return emptyDesc, errOpen
	}
	if errOpen = compressed.Close(); errOpen != nil {
		return emptyDesc, errOpen
	}
	if errOpen = bufW.Flush(); errOpen != nil {
		return emptyDesc, errOpen
	}

	if config.Labels == nil {
		config.Labels = map[string]string{}
	}
	config.Labels[containerdUncompressed] = dgstr.Digest().String()
	var commitopts []content.Opt
	if config.Labels != nil {
		commitopts = append(commitopts, content.WithLabels(config.Labels))
	}
	dgst := cw.Digest()
	if errOpen = cw.Commit(ctx, 0, dgst, commitopts...); errOpen != nil {
		if !errdefs.IsAlreadyExists(errOpen) {
			return emptyDesc, errors.Wrap(errOpen, "failed to commit")
		}
		errOpen = nil
	}

	info, err := d.store.Info(ctx, dgst)
	if err != nil {
		return emptyDesc, errors.Wrap(err, "failed to get info from content store")
	}
	if info.Labels == nil {
		info.Labels = make(map[string]string)
	}

	// Set uncompressed label if digest already existed without label
	if _, ok := info.Labels[containerdUncompressed]; !ok {
		info.Labels[containerdUncompressed] = config.Labels[containerdUncompressed]
		if _, err := d.store.Update(ctx, info, "labels."+containerdUncompressed); err != nil {
			return emptyDesc, errors.Wrap(err, "error setting uncompressed label")
		}
	}

	newDesc := desc
	newDesc.MediaType = mediaType
	newDesc.Digest = info.Digest
	newDesc.Size = info.Size
	return newDesc, nil
}

func uniqueRef() string {
	t := time.Now()
	var b [3]byte
	// Ignore read failures, just decreases uniqueness
	rand.Read(b[:])
	return fmt.Sprintf("%d-%s", t.UnixNano(), base64.URLEncoding.EncodeToString(b[:]))
}
