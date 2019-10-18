package containerimage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/rootfs"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/push"
	"github.com/moby/buildkit/util/resolver"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	keyImageName    = "name"
	keyPush         = "push"
	keyPushByDigest = "push-by-digest"
	keyInsecure     = "registry.insecure"
	keyUnpack       = "unpack"
	ociTypes        = "oci-mediatypes"
)

type Opt struct {
	SessionManager *session.Manager
	ImageWriter    *ImageWriter
	Images         images.Store
	ResolverOpt    resolver.ResolveOptionsFunc
	LeaseManager   leases.Manager
}

type imageExporter struct {
	opt Opt
}

// New returns a new containerimage exporter instance that supports exporting
// to an image store and pushing the image to registry.
// This exporter supports following values in returned kv map:
// - containerimage.digest - The digest of the root manifest for the image.
func New(opt Opt) (exporter.Exporter, error) {
	im := &imageExporter{opt: opt}
	return im, nil
}

func (e *imageExporter) Resolve(ctx context.Context, opt map[string]string) (exporter.ExporterInstance, error) {
	i := &imageExporterInstance{imageExporter: e}
	for k, v := range opt {
		switch k {
		case keyImageName:
			i.targetName = v
		case keyPush:
			if v == "" {
				i.push = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.push = b
		case keyPushByDigest:
			if v == "" {
				i.pushByDigest = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.pushByDigest = b
		case keyInsecure:
			if v == "" {
				i.insecure = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.insecure = b
		case keyUnpack:
			if v == "" {
				i.unpack = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.unpack = b
		case ociTypes:
			if v == "" {
				i.ociTypes = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.ociTypes = b
		default:
			if i.meta == nil {
				i.meta = make(map[string][]byte)
			}
			i.meta[k] = []byte(v)
		}
	}
	return i, nil
}

type imageExporterInstance struct {
	*imageExporter
	targetName   string
	push         bool
	pushByDigest bool
	unpack       bool
	insecure     bool
	ociTypes     bool
	meta         map[string][]byte
}

func (e *imageExporterInstance) Name() string {
	return "exporting to image"
}

func (e *imageExporterInstance) Export(ctx context.Context, src exporter.Source) (map[string]string, error) {
	if src.Metadata == nil {
		src.Metadata = make(map[string][]byte)
	}
	for k, v := range e.meta {
		src.Metadata[k] = v
	}

	ctx, done, err := leaseutil.WithLease(ctx, e.opt.LeaseManager, leaseutil.MakeTemporary)
	if err != nil {
		return nil, err
	}
	defer done(context.TODO())

	desc, err := e.opt.ImageWriter.Commit(ctx, src, e.ociTypes)
	if err != nil {
		return nil, err
	}

	defer func() {
		e.opt.ImageWriter.ContentStore().Delete(context.TODO(), desc.Digest)
	}()

	resp := make(map[string]string)

	if n, ok := src.Metadata["image.name"]; e.targetName == "*" && ok {
		e.targetName = string(n)
	}

	if e.targetName != "" {
		targetNames := strings.Split(e.targetName, ",")
		for _, targetName := range targetNames {
			if e.opt.Images != nil {
				tagDone := oneOffProgress(ctx, "naming to "+targetName)
				img := images.Image{
					Name:      targetName,
					Target:    *desc,
					CreatedAt: time.Now(),
				}

				if _, err := e.opt.Images.Update(ctx, img); err != nil {
					if !errdefs.IsNotFound(err) {
						return nil, tagDone(err)
					}

					if _, err := e.opt.Images.Create(ctx, img); err != nil {
						return nil, tagDone(err)
					}
				}
				tagDone(nil)

				if e.unpack {
					if err := e.unpackImage(ctx, img); err != nil {
						return nil, err
					}
				}
			}
			if e.push {
				if err := push.Push(ctx, e.opt.SessionManager, e.opt.ImageWriter.ContentStore(), desc.Digest, targetName, e.insecure, e.opt.ResolverOpt, e.pushByDigest); err != nil {
					return nil, err
				}
			}
		}
		resp["image.name"] = e.targetName
	}

	resp["containerimage.digest"] = desc.Digest.String()
	return resp, nil
}

func (e *imageExporterInstance) unpackImage(ctx context.Context, img images.Image) (err0 error) {
	unpackDone := oneOffProgress(ctx, "unpacking to "+img.Name)
	defer func() {
		unpackDone(err0)
	}()

	var (
		contentStore = e.opt.ImageWriter.ContentStore()
		applier      = e.opt.ImageWriter.Applier()
		snapshotter  = e.opt.ImageWriter.Snapshotter()
	)

	// fetch manifest by default platform
	manifest, err := images.Manifest(ctx, contentStore, img.Target, platforms.Default())
	if err != nil {
		return err
	}

	layers, err := getLayers(ctx, contentStore, manifest)
	if err != nil {
		return err
	}

	// get containerd snapshotter
	ctrdSnapshotter, release := snapshot.NewContainerdSnapshotter(snapshotter)
	defer release()

	var chain []digest.Digest
	for _, layer := range layers {
		if _, err := rootfs.ApplyLayer(ctx, layer, chain, ctrdSnapshotter, applier); err != nil {
			return err
		}
		chain = append(chain, layer.Diff.Digest)
	}

	var (
		keyGCLabel   = fmt.Sprintf("containerd.io/gc.ref.snapshot.%s", snapshotter.Name())
		valueGCLabel = identity.ChainID(chain).String()
	)

	cinfo := content.Info{
		Digest: manifest.Config.Digest,
		Labels: map[string]string{keyGCLabel: valueGCLabel},
	}
	_, err = contentStore.Update(ctx, cinfo, fmt.Sprintf("labels.%s", keyGCLabel))
	return err
}

func getLayers(ctx context.Context, contentStore content.Store, manifest ocispec.Manifest) ([]rootfs.Layer, error) {
	diffIDs, err := images.RootFS(ctx, contentStore, manifest.Config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve rootfs")
	}

	if len(diffIDs) != len(manifest.Layers) {
		return nil, errors.Errorf("mismatched image rootfs and manifest layers")
	}

	layers := make([]rootfs.Layer, len(diffIDs))
	for i := range diffIDs {
		layers[i].Diff = ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    diffIDs[i],
		}
		layers[i].Blob = manifest.Layers[i]
	}
	return layers, nil
}
