package containerimage

import (
	"context"
	"io"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes"
	"github.com/moby/buildkit/session"
	sessioncontent "github.com/moby/buildkit/session/content"
	"github.com/moby/buildkit/util/imageutil"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	maxReadSize = 4 * 1024 * 1024
)

// getOCILayoutResolver gets a resolver to an OCI layout for a specified store from the client using the given session.
func getOCILayoutResolver(storeID string, sm *session.Manager, sessionID string, g session.Group) *ociLayoutResolver {
	r := &ociLayoutResolver{
		storeID:   storeID,
		sm:        sm,
		sessionID: sessionID,
		g:         g,
	}
	return r
}

type ociLayoutResolver struct {
	remotes.Resolver
	storeID   string
	sm        *session.Manager
	sessionID string
	g         session.Group
}

// Fetcher returns a new fetcher for the provided reference.
func (r *ociLayoutResolver) Fetcher(ctx context.Context, ref string) (remotes.Fetcher, error) {
	return r, nil
}

// Fetch get an io.ReadCloser for the specific content
func (r *ociLayoutResolver) Fetch(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sessionID := r.sessionID

	caller, err := r.sm.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return r.fetchWithAnySession(ctx, desc)
	}

	return r.fetchWithSession(ctx, desc, caller)
}

func (r *ociLayoutResolver) fetchWithAnySession(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := r.sm.Any(ctx, r.g, func(ctx context.Context, _ string, caller session.Caller) error {
		readCloser, err := r.fetchWithSession(ctx, desc, caller)
		if err != nil {
			return err
		}
		rc = readCloser
		return nil
	})
	return rc, err
}

func (r *ociLayoutResolver) fetchWithSession(ctx context.Context, desc ocispecs.Descriptor, caller session.Caller) (io.ReadCloser, error) {
	store := sessioncontent.NewCallerStore(caller, r.storeID)
	readerAt, err := store.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}
	// just wrap the ReaderAt with a Reader
	//return io.NopCloser(&readerAtWrapper{readerAt: readerAt}), nil
	return &readerAtWrapper{readerAt: readerAt}, nil
}

// Resolve attempts to resolve the reference into a name and descriptor.
// OCI Layout does not (yet) support tag name references, but does support hash references.
func (r *ociLayoutResolver) Resolve(ctx context.Context, refString string) (string, ocispecs.Descriptor, error) {
	ref, err := reference.Parse(refString)
	if err != nil {
		return "", ocispecs.Descriptor{}, errors.Wrapf(err, "invalid reference '%s'", refString)
	}
	var (
		dig  digest.Digest
		info content.Info
		rc   io.ReadCloser
	)
	if dig = ref.Digest(); dig == "" {
		return "", ocispecs.Descriptor{}, errors.Errorf("reference must have format @sha256:<hash>: %s", refString)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := r.sm.Get(timeoutCtx, r.sessionID, false)
	if err != nil {
		info, err = r.infoWithAnySession(ctx, dig)
	} else {
		info, err = r.infoWithSession(ctx, dig, caller)
	}
	if err != nil {
		return "", ocispecs.Descriptor{}, errors.Wrap(err, "unable to get info about digest")
	}

	// Create the descriptor, then use that to read the actual root manifest/
	// This is necessary because we do not know the media-type of the descriptor,
	// and there are descriptor processing elements that expect it.
	desc := ocispecs.Descriptor{
		Digest: dig,
		Size:   info.Size,
	}
	caller, err = r.sm.Get(timeoutCtx, r.sessionID, false)
	if err != nil {
		rc, err = r.fetchWithAnySession(ctx, desc)
	} else {
		rc, err = r.fetchWithSession(ctx, desc, caller)
	}
	if err != nil {
		return "", ocispecs.Descriptor{}, errors.Wrap(err, "unable to get root manifest")
	}
	b, err := io.ReadAll(io.LimitReader(rc, maxReadSize))
	if err != nil {
		return "", ocispecs.Descriptor{}, errors.Wrap(err, "unable to read root manifest")
	}
	// try it first as an index, then as a manifest
	mediaType, err := imageutil.DetectManifestBlobMediaType(b)
	if err != nil {
		return "", ocispecs.Descriptor{}, errors.Wrapf(err, "reference %s contains neither an index nor a manifest", refString)
	}
	desc.MediaType = mediaType
	return refString, desc, nil
}

func (r *ociLayoutResolver) infoWithAnySession(ctx context.Context, dig digest.Digest) (content.Info, error) {
	var info content.Info
	err := r.sm.Any(ctx, r.g, func(ctx context.Context, _ string, caller session.Caller) error {
		infoRet, err := r.infoWithSession(ctx, dig, caller)
		if err != nil {
			return err
		}
		info = infoRet
		return nil
	})
	return info, err
}

func (r *ociLayoutResolver) infoWithSession(ctx context.Context, dig digest.Digest, caller session.Caller) (content.Info, error) {
	store := sessioncontent.NewCallerStore(caller, r.storeID)
	info, err := store.Info(ctx, dig)
	if err != nil {
		return info, err
	}
	return info, nil
}

// readerAtWrapper wraps a ReaderAt to give a Reader
type readerAtWrapper struct {
	offset   int64
	readerAt content.ReaderAt
}

func (r *readerAtWrapper) Read(p []byte) (n int, err error) {
	n, err = r.readerAt.ReadAt(p, r.offset)
	r.offset += int64(n)
	return
}
func (r *readerAtWrapper) Close() error {
	return r.readerAt.Close()
}
