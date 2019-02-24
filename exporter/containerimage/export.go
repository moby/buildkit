package containerimage

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/push"
	"github.com/moby/buildkit/util/resolver"
	"github.com/pkg/errors"
)

const (
	keyImageName = "name"
	keyPush      = "push"
	keyInsecure  = "registry.insecure"
	ociTypes     = "oci-mediatypes"
	keySquash    = "squash"
)

type Opt struct {
	SessionManager *session.Manager
	ImageWriter    *ImageWriter
	Images         images.Store
	ResolverOpt    resolver.ResolveOptionsFunc
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
		case keySquash:
			if v == "" {
				i.squash = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.squash = b
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
	targetName string
	push       bool
	insecure   bool
	ociTypes   bool
	squash     bool
	meta       map[string][]byte
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
	desc, err := e.opt.ImageWriter.Commit(ctx, src, e.ociTypes, e.squash)
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
			}
			if e.push {
				if err := push.Push(ctx, e.opt.SessionManager, e.opt.ImageWriter.ContentStore(), desc.Digest, targetName, e.insecure, e.opt.ResolverOpt); err != nil {
					return nil, err
				}
			}
		}
		resp["image.name"] = e.targetName
	}

	resp["containerimage.digest"] = desc.Digest.String()
	return resp, nil
}
