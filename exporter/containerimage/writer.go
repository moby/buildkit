package containerimage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/blobs"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/system"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

const (
	emptyGZLayer = digest.Digest("sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1")
)

type WriterOpt struct {
	Snapshotter  snapshot.Snapshotter
	ContentStore content.Store
	Differ       diff.Differ
}

func NewImageWriter(opt WriterOpt) (*ImageWriter, error) {
	return &ImageWriter{opt: opt}, nil
}

type ImageWriter struct {
	opt WriterOpt
}

func (ic *ImageWriter) Commit(ctx context.Context, ref cache.ImmutableRef, config []byte) (*ocispec.Descriptor, error) {
	layersDone := oneOffProgress(ctx, "exporting layers")
	diffPairs, err := blobs.GetDiffPairs(ctx, ic.opt.ContentStore, ic.opt.Snapshotter, ic.opt.Differ, ref)
	if err != nil {
		return nil, errors.Wrap(err, "failed calculaing diff pairs for exported snapshot")
	}
	layersDone(nil)

	if len(config) == 0 {
		config, err = emptyImageConfig()
		if err != nil {
			return nil, err
		}
	}

	history, err := parseHistoryFromConfig(config)
	if err != nil {
		return nil, err
	}

	diffPairs, history = normalizeLayersAndHistory(diffPairs, history, ref)

	config, err = patchImageConfig(config, diffPairs, history)
	if err != nil {
		return nil, err
	}

	configDigest := digest.FromBytes(config)

	mfst := schema2.Manifest{
		Config: distribution.Descriptor{
			Digest:    configDigest,
			Size:      int64(len(config)),
			MediaType: schema2.MediaTypeImageConfig,
		},
	}
	mfst.SchemaVersion = 2
	mfst.MediaType = schema2.MediaTypeManifest

	labels := map[string]string{
		"containerd.io/gc.ref.content.0": configDigest.String(),
	}

	for i, dp := range diffPairs {
		info, err := ic.opt.ContentStore.Info(ctx, dp.Blobsum)
		if err != nil {
			return nil, errors.Wrapf(err, "could not find blob %s from contentstore", dp.Blobsum)
		}
		mfst.Layers = append(mfst.Layers, distribution.Descriptor{
			Digest:    dp.Blobsum,
			Size:      info.Size,
			MediaType: schema2.MediaTypeLayer,
		})
		labels[fmt.Sprintf("containerd.io/gc.ref.content.%d", i+1)] = dp.Blobsum.String()
	}

	mfstJSON, err := json.Marshal(mfst)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal manifest")
	}

	mfstDigest := digest.FromBytes(mfstJSON)
	mfstDone := oneOffProgress(ctx, "exporting manifest "+mfstDigest.String())

	if err := content.WriteBlob(ctx, ic.opt.ContentStore, mfstDigest.String(), bytes.NewReader(mfstJSON), int64(len(mfstJSON)), mfstDigest, content.WithLabels(labels)); err != nil {
		return nil, mfstDone(errors.Wrapf(err, "error writing manifest blob %s", mfstDigest))
	}
	mfstDone(nil)

	configDone := oneOffProgress(ctx, "exporting config "+configDigest.String())

	if err := content.WriteBlob(ctx, ic.opt.ContentStore, configDigest.String(), bytes.NewReader(config), int64(len(config)), configDigest); err != nil {
		return nil, configDone(errors.Wrap(err, "error writing config blob"))
	}
	configDone(nil)

	// delete config root. config will remain linked to the manifest
	if err := ic.opt.ContentStore.Delete(context.TODO(), configDigest); err != nil {
		return nil, errors.Wrap(err, "error removing config root")
	}

	return &ocispec.Descriptor{
		Digest:    mfstDigest,
		Size:      int64(len(mfstJSON)),
		MediaType: ocispec.MediaTypeImageManifest,
	}, nil
}

func (ic *ImageWriter) ContentStore() content.Store {
	return ic.opt.ContentStore
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
