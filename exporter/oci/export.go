package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	archiveexporter "github.com/containerd/containerd/images/archive"
	"github.com/containerd/containerd/leases"
	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/cache"
	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/progress"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type ExporterVariant string

const (
	VariantOCI    = "oci"
	VariantDocker = "docker"
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
	i := &imageExporterInstance{
		imageExporter: e,
		opts: containerimage.ImageCommitOpts{
			RefCfg: cacheconfig.RefConfig{
				Compression: compression.New(compression.Default),
			},
			BuildInfo: true,
			OCITypes:  e.opt.Variant == VariantOCI,
		},
	}

	opt, err := i.opts.Load(opt)
	if err != nil {
		return nil, err
	}

	for k, v := range opt {
		if i.meta == nil {
			i.meta = make(map[string][]byte)
		}
		i.meta[k] = []byte(v)
	}
	return i, nil
}

type imageExporterInstance struct {
	*imageExporter
	opts containerimage.ImageCommitOpts
	meta map[string][]byte
}

func (e *imageExporterInstance) Name() string {
	return "exporting to oci image format"
}

func (e *imageExporterInstance) Config() exporter.Config {
	return exporter.Config{
		Compression: e.opts.RefCfg.Compression,
	}
}

func (e *imageExporterInstance) Export(ctx context.Context, src *exporter.Source, sessionID string) (map[string]string, error) {
	if e.opt.Variant == VariantDocker && len(src.Refs) > 0 {
		return nil, errors.Errorf("docker exporter does not currently support exporting manifest lists")
	}

	if src.Metadata == nil {
		src.Metadata = make(map[string][]byte)
	}
	for k, v := range e.meta {
		src.Metadata[k] = v
	}

	opts := e.opts
	as, _, err := containerimage.ParseAnnotations(src.Metadata)
	if err != nil {
		return nil, err
	}
	opts.AddAnnotations(as)

	ctx, done, err := leaseutil.WithLease(ctx, e.opt.LeaseManager, leaseutil.MakeTemporary)
	if err != nil {
		return nil, err
	}
	defer done(context.TODO())

	desc, err := e.opt.ImageWriter.Commit(ctx, src, sessionID, &opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		e.opt.ImageWriter.ContentStore().Delete(context.TODO(), desc.Digest)
	}()

	if desc.Annotations == nil {
		desc.Annotations = map[string]string{}
	}
	if _, ok := desc.Annotations[ocispecs.AnnotationCreated]; !ok {
		desc.Annotations[ocispecs.AnnotationCreated] = time.Now().UTC().Format(time.RFC3339)
	}

	resp := make(map[string]string)

	resp[exptypes.ExporterImageDigestKey] = desc.Digest.String()
	if v, ok := desc.Annotations[exptypes.ExporterConfigDigestKey]; ok {
		resp[exptypes.ExporterImageConfigDigestKey] = v
		delete(desc.Annotations, exptypes.ExporterConfigDigestKey)
	}

	dtdesc, err := json.Marshal(desc)
	if err != nil {
		return nil, err
	}
	resp[exptypes.ExporterImageDescriptorKey] = base64.StdEncoding.EncodeToString(dtdesc)

	if n, ok := src.Metadata["image.name"]; e.opts.ImageName == "*" && ok {
		e.opts.ImageName = string(n)
	}

	names, err := normalizedNames(e.opts.ImageName)
	if err != nil {
		return nil, err
	}

	if len(names) != 0 {
		resp["image.name"] = strings.Join(names, ",")
	}

	expOpts := []archiveexporter.ExportOpt{archiveexporter.WithManifest(*desc, names...)}
	switch e.opt.Variant {
	case VariantOCI:
		expOpts = append(expOpts, archiveexporter.WithAllPlatforms(), archiveexporter.WithSkipDockerManifest())
	case VariantDocker:
	default:
		return nil, errors.Errorf("invalid variant %q", e.opt.Variant)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.opt.SessionManager.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, err
	}

	w, err := filesync.CopyFileWriter(ctx, resp, caller)
	if err != nil {
		return nil, err
	}

	mprovider := contentutil.NewMultiProvider(e.opt.ImageWriter.ContentStore())
	if src.Ref != nil {
		remotes, err := src.Ref.GetRemotes(ctx, false, e.opts.RefCfg, false, session.NewGroup(sessionID))
		if err != nil {
			return nil, err
		}
		remote := remotes[0]
		// unlazy before tar export as the tar writer does not handle
		// layer blobs in parallel (whereas unlazy does)
		if unlazier, ok := remote.Provider.(cache.Unlazier); ok {
			if err := unlazier.Unlazy(ctx); err != nil {
				return nil, err
			}
		}
		for _, desc := range remote.Descriptors {
			mprovider.Add(desc.Digest, remote.Provider)
		}
	}
	if len(src.Refs) > 0 {
		for _, r := range src.Refs {
			remotes, err := r.GetRemotes(ctx, false, e.opts.RefCfg, false, session.NewGroup(sessionID))
			if err != nil {
				return nil, err
			}
			remote := remotes[0]
			if unlazier, ok := remote.Provider.(cache.Unlazier); ok {
				if err := unlazier.Unlazy(ctx); err != nil {
					return nil, err
				}
			}
			for _, desc := range remote.Descriptors {
				mprovider.Add(desc.Digest, remote.Provider)
			}
		}
	}

	report := progress.OneOff(ctx, "sending tarball")
	if err := archiveexporter.Export(ctx, mprovider, w, expOpts...); err != nil {
		w.Close()
		if grpcerrors.Code(err) == codes.AlreadyExists {
			return resp, report(nil)
		}
		return nil, report(err)
	}
	err = w.Close()
	if grpcerrors.Code(err) == codes.AlreadyExists {
		return resp, report(nil)
	}
	return resp, report(err)
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
