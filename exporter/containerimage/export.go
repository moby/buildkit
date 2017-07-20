package containerimage

import (
	"bytes"
	gocontext "context"
	"encoding/json"
	"runtime"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/rootfs"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/system"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

const (
	keyImageName = "name"
)

type Opt struct {
	Snapshotter   snapshot.Snapshotter
	ContentStore  content.Store
	Differ        rootfs.MountDiffer
	CacheAccessor cache.Accessor
	MetadataStore metadata.Store
	Images        images.Store
}

type imageExporter struct {
	blobmap blobmapper
	opt     Opt
	g       flightcontrol.Group
}

type diffPair struct {
	diffID  digest.Digest
	blobsum digest.Digest
}

type blobmapper interface {
	GetBlob(ctx gocontext.Context, key string) (digest.Digest, error)
	SetBlob(ctx gocontext.Context, key string, blob digest.Digest) error
}

func New(opt Opt) (exporter.Exporter, error) {
	blobmap, ok := opt.Snapshotter.(blobmapper)
	if !ok {
		return nil, errors.Errorf("image exporter requires snapshotter with blobs mapping support")
	}

	im := &imageExporter{opt: opt, blobmap: blobmap}
	return im, nil
}

func (e *imageExporter) Resolve(ctx context.Context, opt map[string]string) (exporter.ExporterInstance, error) {
	i := &imageExporterInstance{imageExporter: e}
	for k, v := range opt {
		switch k {
		case keyImageName:
			i.targetName = v
		default:
			logrus.Warnf("unknown exporter option %s", k)
		}
	}
	return i, nil
}

func (e *imageExporter) getBlobs(ctx context.Context, ref cache.ImmutableRef) ([]diffPair, error) {
	eg, ctx := errgroup.WithContext(ctx)
	var diffPairs []diffPair
	var currentPair diffPair
	parent := ref.Parent()
	if parent != nil {
		defer parent.Release(context.TODO())
		eg.Go(func() error {
			dp, err := e.getBlobs(ctx, parent)
			if err != nil {
				return err
			}
			diffPairs = dp
			return nil
		})
	}
	eg.Go(func() error {
		dp, err := e.g.Do(ctx, ref.ID(), func(ctx context.Context) (interface{}, error) {
			blob, err := e.blobmap.GetBlob(ctx, ref.ID())
			if err != nil {
				return nil, err
			}
			if blob != "" {
				diffID, err := digest.Parse(ref.ID())
				if err != nil {
					diffID = blob
				}
				return diffPair{diffID: diffID, blobsum: blob}, nil
			}
			// reference needs to be committed
			parent := ref.Parent()
			var lower []mount.Mount
			if parent != nil {
				defer parent.Release(context.TODO())
				lower, err = parent.Mount(ctx, true)
				if err != nil {
					return nil, err
				}
			}
			upper, err := ref.Mount(ctx, true)
			if err != nil {
				return nil, err
			}
			descr, err := e.opt.Differ.DiffMounts(ctx, lower, upper, ocispec.MediaTypeImageLayer, ref.ID())
			if err != nil {
				return nil, err
			}
			if err := e.blobmap.SetBlob(ctx, ref.ID(), descr.Digest); err != nil {
				return nil, err
			}
			return diffPair{diffID: descr.Digest, blobsum: descr.Digest}, nil
		})
		if err != nil {
			return err
		}
		currentPair = dp.(diffPair)
		return nil
	})
	err := eg.Wait()
	if err != nil {
		return nil, err
	}
	return append(diffPairs, currentPair), nil
}

type imageExporterInstance struct {
	*imageExporter
	targetName string
}

func (e *imageExporterInstance) Name() string {
	return "exporting to image"
}

func (e *imageExporterInstance) Export(ctx context.Context, ref cache.ImmutableRef) error {
	layersDone := oneOffProgress(ctx, "exporting layers")
	diffPairs, err := e.getBlobs(ctx, ref)
	if err != nil {
		return err
	}
	layersDone(nil)

	diffIDs := make([]digest.Digest, 0, len(diffPairs))
	for _, dp := range diffPairs {
		diffIDs = append(diffIDs, dp.diffID)
	}

	dt, err := json.Marshal(imageConfig(diffIDs))
	if err != nil {
		return errors.Wrap(err, "failed to marshal image config")
	}

	dgst := digest.FromBytes(dt)
	configDone := oneOffProgress(ctx, "exporting config "+dgst.String())

	if err := content.WriteBlob(ctx, e.opt.ContentStore, dgst.String(), bytes.NewReader(dt), int64(len(dt)), dgst); err != nil {
		return configDone(errors.Wrap(err, "error writing config blob"))
	}
	configDone(nil)

	mfst := ocispec.Manifest{
		Config: ocispec.Descriptor{
			Digest:    dgst,
			Size:      int64(len(dt)),
			MediaType: ocispec.MediaTypeImageConfig,
		},
	}
	mfst.SchemaVersion = 2

	for _, dp := range diffPairs {
		info, err := e.opt.ContentStore.Info(ctx, dp.blobsum)
		if err != nil {
			return configDone(errors.Wrapf(err, "could not get blob %s", dp.blobsum))
		}
		mfst.Layers = append(mfst.Layers, ocispec.Descriptor{
			Digest:    dp.blobsum,
			Size:      info.Size,
			MediaType: ocispec.MediaTypeImageLayerGzip,
		})
	}

	dt, err = json.Marshal(mfst)
	if err != nil {
		return errors.Wrap(err, "failed to marshal manifest")
	}

	dgst = digest.FromBytes(dt)
	mfstDone := oneOffProgress(ctx, "exporting manifest "+dgst.String())

	if err := content.WriteBlob(ctx, e.opt.ContentStore, dgst.String(), bytes.NewReader(dt), int64(len(dt)), dgst); err != nil {
		return mfstDone(errors.Wrap(err, "error writing manifest blob"))
	}

	mfstDone(nil)

	if e.opt.Images != nil && e.targetName != "" {
		tagDone := oneOffProgress(ctx, "naming to "+e.targetName)
		imgrec := images.Image{
			Name: e.targetName,
			Target: ocispec.Descriptor{
				Digest:    dgst,
				Size:      int64(len(dt)),
				MediaType: ocispec.MediaTypeImageManifest,
			},
			CreatedAt: time.Now(),
		}
		_, err := e.opt.Images.Update(ctx, imgrec)
		if err != nil {
			if !errdefs.IsNotFound(err) {
				return tagDone(err)
			}

			_, err := e.opt.Images.Create(ctx, imgrec)
			if err != nil {
				return tagDone(err)
			}
		}
		tagDone(nil)
	}

	return err
}

// this is temporary: should move to dockerfile frontend
func imageConfig(diffIDs []digest.Digest) ocispec.Image {
	img := ocispec.Image{
		Architecture: runtime.GOARCH,
		OS:           runtime.GOOS,
	}
	img.RootFS.Type = "layers"
	img.RootFS.DiffIDs = diffIDs
	img.Config.WorkingDir = "/"
	img.Config.Env = []string{"PATH=" + system.DefaultPathEnv}
	return img
}

func oneOffProgress(ctx context.Context, id string) func(err error) error {
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func(err error) error {
		// TODO: set error on status
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
		return err
	}
}
