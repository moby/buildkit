package containerimage

import (
	"bytes"
	"encoding/json"
	"runtime"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/rootfs"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/blobs"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/push"
	"github.com/moby/buildkit/util/system"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

const (
	keyImageName        = "name"
	keyPush             = "push"
	exporterImageConfig = "containerimage.config"
)

type Opt struct {
	Snapshotter  snapshot.Snapshotter
	ContentStore content.Store
	Differ       rootfs.MountDiffer
	Images       images.Store
}

type imageExporter struct {
	opt Opt
}

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
			i.push = true
		default:
			logrus.Warnf("unknown exporter option %s", k)
		}
	}
	return i, nil
}

type imageExporterInstance struct {
	*imageExporter
	targetName string
	push       bool
}

func (e *imageExporterInstance) Name() string {
	return "exporting to image"
}

func (e *imageExporterInstance) Export(ctx context.Context, ref cache.ImmutableRef, opt map[string][]byte) error {
	layersDone := oneOffProgress(ctx, "exporting layers")
	diffPairs, err := blobs.GetDiffPairs(ctx, e.opt.Snapshotter, e.opt.Differ, ref)
	if err != nil {
		return err
	}
	layersDone(nil)

	diffIDs := make([]digest.Digest, 0, len(diffPairs))
	for _, dp := range diffPairs {
		diffIDs = append(diffIDs, dp.DiffID)
	}

	var dt []byte
	if config, ok := opt[exporterImageConfig]; ok {
		dt, err = setDiffIDs(config, diffIDs)
		if err != nil {
			return err
		}
	} else {
		dt, err = json.Marshal(imageConfig(diffIDs))
		if err != nil {
			return errors.Wrap(err, "failed to marshal image config")
		}
	}

	dgst := digest.FromBytes(dt)
	configDone := oneOffProgress(ctx, "exporting config "+dgst.String())

	if err := content.WriteBlob(ctx, e.opt.ContentStore, dgst.String(), bytes.NewReader(dt), int64(len(dt)), dgst); err != nil {
		return configDone(errors.Wrap(err, "error writing config blob"))
	}
	configDone(nil)

	mfst := schema2.Manifest{
		Config: distribution.Descriptor{
			Digest:    dgst,
			Size:      int64(len(dt)),
			MediaType: schema2.MediaTypeImageConfig,
		},
	}
	mfst.SchemaVersion = 2
	mfst.MediaType = schema2.MediaTypeManifest

	for _, dp := range diffPairs {
		info, err := e.opt.ContentStore.Info(ctx, dp.Blobsum)
		if err != nil {
			return configDone(errors.Wrapf(err, "could not get blob %s", dp.Blobsum))
		}
		mfst.Layers = append(mfst.Layers, distribution.Descriptor{
			Digest:    dp.Blobsum,
			Size:      info.Size,
			MediaType: schema2.MediaTypeLayer,
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

	if e.targetName != "" {
		if e.opt.Images != nil {
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
		if e.push {
			return push.Push(ctx, e.opt.ContentStore, dgst, e.targetName)
		}
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

func setDiffIDs(config []byte, diffIDs []digest.Digest) ([]byte, error) {
	mp := map[string]json.RawMessage{}
	if err := json.Unmarshal(config, &mp); err != nil {
		return nil, err
	}
	var rootFS ocispec.RootFS
	rootFS.Type = "layers"
	rootFS.DiffIDs = diffIDs
	dt, err := json.Marshal(rootFS)
	if err != nil {
		return nil, err
	}
	mp["rootfs"] = dt
	return json.Marshal(mp)
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
