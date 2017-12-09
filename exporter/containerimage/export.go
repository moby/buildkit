package containerimage

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/blobs"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/session"
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
	keyInsecure         = "registry.insecure"
	exporterImageConfig = "containerimage.config"

	emptyGZLayer = digest.Digest("sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1")
)

type Opt struct {
	SessionManager *session.Manager
	Snapshotter    snapshot.Snapshotter
	ContentStore   content.Store
	Differ         diff.Differ
	Images         images.Store
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
		case keyInsecure:
			i.insecure = true
		default:
			logrus.Warnf("image exporter: unknown option %s", k)
		}
	}
	return i, nil
}

type imageExporterInstance struct {
	*imageExporter
	targetName string
	push       bool
	insecure   bool
}

func (e *imageExporterInstance) Name() string {
	return "exporting to image"
}

func (e *imageExporterInstance) Export(ctx context.Context, ref cache.ImmutableRef, opt map[string][]byte) error {
	layersDone := oneOffProgress(ctx, "exporting layers")
	diffPairs, err := blobs.GetDiffPairs(ctx, e.opt.ContentStore, e.opt.Snapshotter, e.opt.Differ, ref)
	if err != nil {
		return errors.Wrap(err, "failed calculaing diff pairs for exported snapshot")
	}
	layersDone(nil)

	config, ok := opt[exporterImageConfig]
	if !ok {
		config, err = emptyImageConfig()
		if err != nil {
			return err
		}
	}

	history, err := parseHistoryFromConfig(config)
	if err != nil {
		return err
	}

	diffPairs, history = normalizeLayersAndHistory(diffPairs, history, ref)

	config, err = patchImageConfig(config, diffPairs, history)
	if err != nil {
		return err
	}

	addAsRoot := content.WithLabels(map[string]string{
		"containerd.io/gc.root": time.Now().UTC().Format(time.RFC3339Nano),
	})

	configDigest := digest.FromBytes(config)
	configDone := oneOffProgress(ctx, "exporting config "+configDigest.String())

	if err := content.WriteBlob(ctx, e.opt.ContentStore, configDigest.String(), bytes.NewReader(config), int64(len(config)), configDigest, addAsRoot); err != nil {
		return configDone(errors.Wrap(err, "error writing config blob"))
	}
	configDone(nil)

	mfst := schema2.Manifest{
		Config: distribution.Descriptor{
			Digest:    configDigest,
			Size:      int64(len(config)),
			MediaType: schema2.MediaTypeImageConfig,
		},
	}
	mfst.SchemaVersion = 2
	mfst.MediaType = schema2.MediaTypeManifest

	for _, dp := range diffPairs {
		info, err := e.opt.ContentStore.Info(ctx, dp.Blobsum)
		if err != nil {
			return configDone(errors.Wrapf(err, "could not find blob %s from contentstore", dp.Blobsum))
		}
		mfst.Layers = append(mfst.Layers, distribution.Descriptor{
			Digest:    dp.Blobsum,
			Size:      info.Size,
			MediaType: schema2.MediaTypeLayer,
		})
	}

	mfstJSON, err := json.Marshal(mfst)
	if err != nil {
		return errors.Wrap(err, "failed to marshal manifest")
	}

	mfstDigest := digest.FromBytes(mfstJSON)
	mfstDone := oneOffProgress(ctx, "exporting manifest "+mfstDigest.String())

	if err := content.WriteBlob(ctx, e.opt.ContentStore, mfstDigest.String(), bytes.NewReader(mfstJSON), int64(len(mfstJSON)), mfstDigest, addAsRoot); err != nil {
		return mfstDone(errors.Wrapf(err, "error writing manifest blob %s", mfstDigest))
	}
	mfstDone(nil)

	if e.targetName != "" {
		if e.opt.Images != nil {
			tagDone := oneOffProgress(ctx, "naming to "+e.targetName)
			img := images.Image{
				Name: e.targetName,
				Target: ocispec.Descriptor{
					Digest:    mfstDigest,
					Size:      int64(len(mfstJSON)),
					MediaType: ocispec.MediaTypeImageManifest,
				},
				CreatedAt: time.Now(),
			}

			if _, err := e.opt.Images.Update(ctx, img); err != nil {
				if !errdefs.IsNotFound(err) {
					return tagDone(err)
				}

				if _, err := e.opt.Images.Create(ctx, img); err != nil {
					return tagDone(err)
				}
			}
			tagDone(nil)
		}
		if e.push {
			return push.Push(ctx, e.opt.SessionManager, e.opt.ContentStore, mfstDigest, e.targetName, e.insecure)
		}
	}

	return nil
}

func emptyImageConfig() ([]byte, error) {
	img := ocispec.Image{
		Architecture: runtime.GOARCH,
		OS:           runtime.GOOS,
	}
	img.RootFS.Type = "layers"
	img.Config.WorkingDir = "/"
	img.Config.Env = []string{"PATH=" + system.DefaultPathEnv}
	dt, err := json.Marshal(img)
	return dt, errors.Wrap(err, "failed to create empty image config")
}

func parseHistoryFromConfig(dt []byte) ([]ocispec.History, error) {
	var config struct {
		History []ocispec.History
	}
	if err := json.Unmarshal(dt, &config); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal history from config")
	}
	return config.History, nil
}

func patchImageConfig(dt []byte, dps []blobs.DiffPair, history []ocispec.History) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(dt, &m); err != nil {
		return nil, errors.Wrap(err, "failed to parse image config for patch")
	}

	var rootFS ocispec.RootFS
	rootFS.Type = "layers"
	for _, dp := range dps {
		rootFS.DiffIDs = append(rootFS.DiffIDs, dp.DiffID)
	}
	dt, err := json.Marshal(rootFS)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal rootfs")
	}
	m["rootfs"] = dt

	dt, err = json.Marshal(history)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal history")
	}
	m["history"] = dt

	dt, err = json.Marshal(m)
	return dt, errors.Wrap(err, "failed to marshal config after patch")
}

func normalizeLayersAndHistory(diffs []blobs.DiffPair, history []ocispec.History, ref cache.ImmutableRef) ([]blobs.DiffPair, []ocispec.History) {
	var historyLayers int
	for _, h := range history {
		if !h.EmptyLayer {
			historyLayers += 1
		}
	}
	if historyLayers > len(diffs) {
		// this case shouldn't happen but if it does force set history layers empty
		// from the bottom
		logrus.Warn("invalid image config with unaccounted layers")
		historyCopy := make([]ocispec.History, 0, len(history))
		var l int
		for _, h := range history {
			if l >= len(diffs) {
				h.EmptyLayer = true
			}
			if !h.EmptyLayer {
				l++
			}
			historyCopy = append(historyCopy, h)
		}
		history = historyCopy
	}

	if len(diffs) > historyLayers {
		// some history items are missing. add them based on the ref metadata
		for _, msg := range getRefDesciptions(ref, len(diffs)-historyLayers) {
			tm := time.Now().UTC()
			history = append(history, ocispec.History{
				Created:   &tm,
				CreatedBy: msg,
				Comment:   "buildkit.exporter.image.v0",
			})
		}
	}

	var layerIndex int
	for i, h := range history {
		if !h.EmptyLayer {
			if diffs[layerIndex].Blobsum == emptyGZLayer {
				h.EmptyLayer = true
				diffs = append(diffs[:layerIndex], diffs[layerIndex+1:]...)
			} else {
				layerIndex++
			}
		}
		history[i] = h
	}

	return diffs, history
}

func getRefDesciptions(ref cache.ImmutableRef, limit int) []string {
	if limit <= 0 {
		return nil
	}
	defaultMsg := "created by buildkit" // shouldn't happen but don't fail build
	if ref == nil {
		strings.Repeat(defaultMsg, limit)
	}
	descr := cache.GetDescription(ref.Metadata())
	if descr == "" {
		descr = defaultMsg
	}
	return append(getRefDesciptions(ref.Parent(), limit-1), descr)
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
