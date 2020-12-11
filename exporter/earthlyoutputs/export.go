package earthlyoutputs

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

	archiveexporter "github.com/containerd/containerd/images/archive"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/push"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
)

type ExporterVariant string

const (
	keyImageName        = "name"
	keyLayerCompression = "compression"
	VariantOCI          = "oci"
	VariantDocker       = "docker"
	ociTypes            = "oci-mediatypes"
)

type Opt struct {
	SessionManager *session.Manager
	ImageWriter    *containerimage.ImageWriter
	Variant        ExporterVariant
	RegistryHosts  docker.RegistryHosts
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
	var ot *bool
	i := &imageExporterInstance{
		imageExporter:    e,
		layerCompression: compression.Default,
	}
	for k, v := range opt {
		switch k {
		case keyImageName:
			i.name = v
		case keyLayerCompression:
			switch v {
			case "gzip":
				i.layerCompression = compression.Gzip
			case "uncompressed":
				i.layerCompression = compression.Uncompressed
			default:
				return nil, errors.Errorf("unsupported layer compression type: %v", v)
			}
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
	meta             map[string][]byte
	name             string
	ociTypes         bool
	layerCompression compression.Type
}

func (e *imageExporterInstance) Name() string {
	return "[output] exporting outputs"
}

func (e *imageExporterInstance) Export(ctx context.Context, src exporter.Source, sessionID string) (map[string]string, error) {
	if src.Ref != nil {
		return nil, errors.Errorf("export with src.Ref not supported")
	}
	if src.Metadata == nil {
		src.Metadata = make(map[string][]byte)
	}
	for k, v := range e.meta {
		src.Metadata[k] = v
	}
	dirs := make(map[string]bool)
	images := make(map[string]bool)
	shouldPush := make(map[string]bool)
	allImages := make(map[string]bool) // images + shouldPush union
	expSrcs := make(map[string]exporter.Source)
	for k, ref := range src.Refs {
		expMd := make(map[string][]byte)
		mdPrefix := fmt.Sprintf("ref/%s/", k)
		for mdK, mdV := range src.Metadata {
			if strings.HasPrefix(mdK, mdPrefix) {
				expMd[strings.TrimPrefix(mdK, mdPrefix)] = mdV
			}
		}
		if string(expMd["export-image"]) == "true" {
			allImages[k] = true
			images[k] = true
		}
		if string(expMd["export-dir"]) == "true" {
			dirs[k] = true
		}
		if string(expMd["export-image-push"]) == "true" {
			allImages[k] = true
			shouldPush[k] = true
		}
		inlineCache, ok := src.Metadata[fmt.Sprintf("%s/%s", exptypes.ExporterInlineCache, k)]
		if ok {
			expMd[exptypes.ExporterInlineCache] = inlineCache
		}
		expSrc := exporter.Source{
			Ref:      ref,
			Refs:     map[string]cache.ImmutableRef{},
			Metadata: expMd,
		}
		expSrcs[k] = expSrc
	}

	ctx, done, err := leaseutil.WithLease(ctx, e.opt.LeaseManager, leaseutil.MakeTemporary)
	if err != nil {
		return nil, err
	}
	defer done(context.TODO())

	descs := make(map[string]*ocispec.Descriptor)
	names := make(map[string][]string)
	for k := range allImages {
		expSrc := expSrcs[k]
		desc, err := e.opt.ImageWriter.Commit(ctx, expSrc, e.ociTypes, e.layerCompression, sessionID)
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
		descs[k] = desc

		imgName := e.name
		if n, ok := expSrc.Metadata["image.name"]; ok {
			imgName = string(n)
		}
		imgNames, err := normalizedNames(imgName)
		if err != nil {
			return nil, err
		}
		names[k] = imgNames
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.opt.SessionManager.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, err
	}

	resp := make(map[string]string)
	// TODO(vladaionescu): Fill resp

	writers := make(map[string]io.WriteCloser)
	for k := range allImages {
		md := make(map[string]string)
		for mdK, mdV := range expSrcs[k].Metadata {
			md[mdK] = string(mdV)
		}
		md["containerimage.digest"] = descs[k].Digest.String()
		if len(names[k]) != 0 {
			md["image.name"] = strings.Join(names[k], ",")
		}

		w, err := filesync.CopyFileWriter(ctx, md, caller)
		if err != nil {
			return nil, err
		}
		writers[k] = w
	}
	eg, egCtx := errgroup.WithContext(ctx)
	for k := range dirs {
		md := make(map[string]string)
		for mdK, mdV := range expSrcs[k].Metadata {
			md[mdK] = string(mdV)
		}
		eg.Go(exportDirFunc(egCtx, md, caller, expSrcs[k].Ref, sessionID))
	}

	mproviders := make(map[string]*contentutil.MultiProvider)
	annotations := map[digest.Digest]map[string]string{}
	for k := range allImages {
		expSrc := expSrcs[k]
		mprovider := contentutil.NewMultiProvider(e.opt.ImageWriter.ContentStore())
		if expSrc.Ref != nil {
			remote, err := expSrc.Ref.GetRemote(ctx, false, e.layerCompression, session.NewGroup(sessionID))
			if err != nil {
				return nil, err
			}
			// unlazy before tar export as the tar writer does not handle
			// layer blobs in parallel (whereas unlazy does)
			if unlazier, ok := remote.Provider.(cache.Unlazier); ok {
				if err := unlazier.Unlazy(ctx); err != nil {
					return nil, err
				}
			}
			for _, desc := range remote.Descriptors {
				mprovider.Add(desc.Digest, remote.Provider)
				addAnnotations(annotations, desc)
			}

			if shouldPush[k] {
				for _, name := range names[k] {
					err := push.Push(
						ctx, e.opt.SessionManager, sessionID, mprovider,
						e.opt.ImageWriter.ContentStore(), descs[k].Digest,
						name, false, e.opt.RegistryHosts, false, annotations)
					if err != nil {
						return nil, err
					}
				}
			}
		}
		mproviders[k] = mprovider
	}

	report := oneOffProgress(ctx, "sending tarballs")
	for k := range images {
		w := writers[k]
		desc := descs[k]
		expOpts := []archiveexporter.ExportOpt{archiveexporter.WithManifest(*desc, names[k]...)}
		switch e.opt.Variant {
		case VariantOCI:
			expOpts = append(expOpts, archiveexporter.WithAllPlatforms(), archiveexporter.WithSkipDockerManifest())
		case VariantDocker:
		default:
			return nil, report(errors.Errorf("invalid variant %q", e.opt.Variant))
		}
		if err := archiveexporter.Export(ctx, mproviders[k], w, expOpts...); err != nil {
			w.Close()
			if grpcerrors.Code(err) == codes.AlreadyExists {
				continue
			}
			if errors.Is(err, io.EOF) {
				// TODO(vladaionescu): This sometimes happens when server responds with
				//                     GRPC code codes.AlreadyExists and
				//                     we continue to try to send data.
				continue
			}
			return nil, report(err)
		}
		err = w.Close()
		if grpcerrors.Code(err) == codes.AlreadyExists {
			continue
		}
		if errors.Is(err, io.EOF) {
			// TODO(vladaionescu): This sometimes happens when server responds with
			//                     GRPC code codes.AlreadyExists and
			//                     we continue to try to send data.
			continue
		}
		if err != nil {
			return nil, report(err)
		}
	}
	if err := eg.Wait(); err != nil {
		return nil, report(err)
	}
	return resp, report(nil)
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

func newProgressHandler(ctx context.Context, id string) func(int, bool) {
	limiter := rate.NewLimiter(rate.Every(100*time.Millisecond), 1)
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
		Action:  "transferring",
	}
	pw.Write(id, st)
	return func(s int, last bool) {
		if last || limiter.Allow() {
			st.Current = s
			if last {
				now := time.Now()
				st.Completed = &now
			}
			pw.Write(id, st)
			if last {
				pw.Close()
			}
		}
	}
}

func exportDirFunc(ctx context.Context, md map[string]string, caller session.Caller, ref cache.ImmutableRef, sessionID string) func() error {
	return func() error {
		var src string
		var err error
		var idmap *idtools.IdentityMapping
		if ref == nil {
			src, err = ioutil.TempDir("", "buildkit")
			if err != nil {
				return err
			}
			defer os.RemoveAll(src)
		} else {
			mount, err := ref.Mount(ctx, true, session.NewGroup(sessionID))
			if err != nil {
				return err
			}

			lm := snapshot.LocalMounter(mount)

			src, err = lm.Mount()
			if err != nil {
				return err
			}

			idmap = mount.IdentityMapping()

			defer lm.Unmount()
		}

		walkOpt := &fsutil.WalkOpt{}

		if idmap != nil {
			walkOpt.Map = func(p string, st *fstypes.Stat) bool {
				uid, gid, err := idmap.ToContainer(idtools.Identity{
					UID: int(st.Uid),
					GID: int(st.Gid),
				})
				if err != nil {
					return false
				}
				st.Uid = uint32(uid)
				st.Gid = uint32(gid)
				return true
			}
		}

		fs := fsutil.NewFS(src, walkOpt)
		progress := newProgressHandler(ctx, "copying files")
		if err := filesync.CopyToCallerWithMeta(ctx, md, fs, caller, progress); err != nil {
			if grpcerrors.Code(err) == codes.AlreadyExists {
				return nil
			}
			if errors.Is(err, io.EOF) {
				// TODO(vladaionescu): This sometimes happens when server responds with
				//                     GRPC code codes.AlreadyExists and
				//                     fsutil continues to try to send data.
				return nil
			}
			return err
		}
		return nil
	}
}

func addAnnotations(m map[digest.Digest]map[string]string, desc ocispec.Descriptor) {
	if desc.Annotations == nil {
		return
	}
	a, ok := m[desc.Digest]
	if !ok {
		m[desc.Digest] = desc.Annotations
		return
	}
	for k, v := range desc.Annotations {
		a[k] = v
	}
}
