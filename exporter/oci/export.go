package oci

import (
	"context"
	"time"

	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/images/oci"
	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/dockerexporter"
	"github.com/moby/buildkit/util/progress"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type ExporterVariant string

const (
	exporterImageConfig = "containerimage.config"
	keyImageName        = "name"
	VariantOCI          = "oci"
	VariantDocker       = "docker"
)

type Opt struct {
	SessionManager *session.Manager
	ImageWriter    *containerimage.ImageWriter
	Variant        ExporterVariant
}

type imageExporter struct {
	opt Opt
}

func New(opt Opt) (exporter.Exporter, error) {
	im := &imageExporter{opt: opt}
	return im, nil
}

func (e *imageExporter) Resolve(ctx context.Context, opt map[string]string) (exporter.ExporterInstance, error) {
	id := session.FromContext(ctx)
	if id == "" {
		return nil, errors.New("could not access local files without session")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.opt.SessionManager.Get(timeoutCtx, id)
	if err != nil {
		return nil, err
	}

	i := &imageExporterInstance{imageExporter: e, caller: caller}
	for k, v := range opt {
		switch k {
		case exporterImageConfig:
			i.config = []byte(v)
		case keyImageName:
			parsed, err := reference.ParseNormalizedNamed(v)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to parse %s", v)
			}
			i.name = reference.TagNameOnly(parsed).String()
		default:
			logrus.Warnf("oci exporter: unknown option %s", k)
		}
	}
	return i, nil
}

type imageExporterInstance struct {
	*imageExporter
	config []byte
	caller session.Caller
	name   string
}

func (e *imageExporterInstance) Name() string {
	return "exporting to oci image format"
}

func (e *imageExporterInstance) Export(ctx context.Context, ref cache.ImmutableRef, opt map[string][]byte) (map[string]string, error) {
	if config, ok := opt[exporterImageConfig]; ok {
		e.config = config
	}
	desc, err := e.opt.ImageWriter.Commit(ctx, ref, e.config)
	if err != nil {
		return nil, err
	}
	defer func() {
		e.opt.ImageWriter.ContentStore().Delete(context.TODO(), desc.Digest)
	}()
	if desc.Annotations == nil {
		desc.Annotations = map[string]string{}
	}
	desc.Annotations[ocispec.AnnotationCreated] = time.Now().UTC().Format(time.RFC3339)

	exp, err := getExporter(e.opt.Variant, e.name)
	if err != nil {
		return nil, err
	}

	w, err := filesync.CopyFileWriter(ctx, e.caller)
	if err != nil {
		return nil, err
	}
	report := oneOffProgress(ctx, "sending tarball")
	if err := exp.Export(ctx, e.opt.ImageWriter.ContentStore(), *desc, w); err != nil {
		w.Close()
		return nil, report(err)
	}
	return nil, report(w.Close())
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

func getExporter(variant ExporterVariant, name string) (images.Exporter, error) {
	switch variant {
	case VariantOCI:
		return &oci.V1Exporter{}, nil
	case VariantDocker:
		return &dockerexporter.DockerExporter{Name: name}, nil
	default:
		return nil, errors.Errorf("invalid variant %q", variant)
	}
}
