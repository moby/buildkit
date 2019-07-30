package oci

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/images/oci"
	"github.com/containerd/containerd/leases"
	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/dockerexporter"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/progress"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ExporterVariant string

const (
	keyImageName  = "name"
	VariantOCI    = "oci"
	VariantDocker = "docker"
	ociTypes      = "oci-mediatypes"
)

type Opt struct {
	SessionManager *session.Manager
	ImageWriter    *containerimage.ImageWriter
	Variant        ExporterVariant
	LeaseManager   leases.Manager
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

	var ot *bool
	i := &imageExporterInstance{imageExporter: e, caller: caller}
	for k, v := range opt {
		switch k {
		case keyImageName:
			i.name = v
		case ociTypes:
			ot = new(bool)
			if v == "" {
				*ot = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			*ot = b
		default:
			if i.meta == nil {
				i.meta = make(map[string][]byte)
			}
			i.meta[k] = []byte(v)
		}
	}
	if ot == nil {
		i.ociTypes = e.opt.Variant == VariantOCI
	} else {
		i.ociTypes = *ot
	}
	return i, nil
}

type imageExporterInstance struct {
	*imageExporter
	meta     map[string][]byte
	caller   session.Caller
	name     string
	ociTypes bool
}

func (e *imageExporterInstance) Name() string {
	return "exporting to oci image format"
}

func (e *imageExporterInstance) Export(ctx context.Context, src exporter.Source) (map[string]string, error) {
	if e.opt.Variant == VariantDocker && len(src.Refs) > 0 {
		return nil, errors.Errorf("docker exporter does not currently support exporting manifest lists")
	}

	if src.Metadata == nil {
		src.Metadata = make(map[string][]byte)
	}
	for k, v := range e.meta {
		src.Metadata[k] = v
	}

	ctx, done, err := leaseutil.WithLease(ctx, e.opt.LeaseManager)
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
	if desc.Annotations == nil {
		desc.Annotations = map[string]string{}
	}
	desc.Annotations[ocispec.AnnotationCreated] = time.Now().UTC().Format(time.RFC3339)

	resp := make(map[string]string)
	resp["containerimage.digest"] = desc.Digest.String()

	if n, ok := src.Metadata["image.name"]; e.name == "*" && ok {
		e.name = string(n)
	}

	names, err := normalizedNames(e.name)
	if err != nil {
		return nil, err
	}

	if len(names) != 0 {
		resp["image.name"] = strings.Join(names, ",")
	}

	exp, err := getExporter(e.opt.Variant, names)
	if err != nil {
		return nil, err
	}

	w, err := filesync.CopyFileWriter(ctx, resp, e.caller)
	if err != nil {
		return nil, err
	}
	report := oneOffProgress(ctx, "sending tarball")
	if err := exp.Export(ctx, e.opt.ImageWriter.ContentStore(), *desc, w); err != nil {
		w.Close()
		if st, ok := status.FromError(errors.Cause(err)); ok && st.Code() == codes.AlreadyExists {
			return resp, report(nil)
		}
		return nil, report(err)
	}
	err = w.Close()
	if st, ok := status.FromError(errors.Cause(err)); ok && st.Code() == codes.AlreadyExists {
		return resp, report(nil)
	}
	return resp, report(err)
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

func getExporter(variant ExporterVariant, names []string) (images.Exporter, error) {
	switch variant {
	case VariantOCI:
		if len(names) != 0 {
			return nil, errors.New("oci exporter cannot export named image")
		}
		return oci.ResolveV1ExportOpt(oci.WithAllPlatforms(true))
	case VariantDocker:
		return &dockerexporter.DockerExporter{Names: names}, nil
	default:
		return nil, errors.Errorf("invalid variant %q", variant)
	}
}

func normalizedNames(name string) ([]string, error) {
	if name == "" {
		return nil, nil
	}
	names := strings.Split(name, ",")
	var tagNames = make([]string, len(names))
	for i, name := range names {
		parsed, err := reference.ParseNormalizedNamed(name)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse %s", name)
		}
		tagNames[i] = reference.TagNameOnly(parsed).String()
	}
	return tagNames, nil
}
