package earthlyoutputs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

	archiveexporter "github.com/containerd/containerd/images/archive"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/platforms"
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
	localExport := make(map[string]bool)              // imgName -> true/false
	shouldPush := make(map[string]bool)               // imgName -> true/false
	insecurePush := make(map[string]bool)             // imgName -> true/false
	plats := make(map[string][]exptypes.Platform)     // imgName -> []platform
	imageExpSrcs := make(map[string]*exporter.Source) // imgName -> expSrc
	var dirExpSrcs []*exporter.Source
	for k, ref := range src.Refs {
		simpleMd := make(map[string][]byte)
		mdPrefix := fmt.Sprintf("ref/%s/", k)
		for mdK, mdV := range src.Metadata {
			if strings.HasPrefix(mdK, mdPrefix) {
				simpleMd[strings.TrimPrefix(mdK, mdPrefix)] = mdV
			}
		}
		inlineCacheK := fmt.Sprintf("%s/%s", exptypes.ExporterInlineCache, k)
		inlineCache, ok := src.Metadata[inlineCacheK]
		if ok {
			simpleMd[exptypes.ExporterInlineCache] = inlineCache
		}

		le := false
		isImage := false
		if string(simpleMd["export-image"]) == "true" {
			isImage = true
			le = true
		}
		sp := false
		if string(simpleMd["export-image-push"]) == "true" {
			isImage = true
			sp = true
		}
		ip := false
		if string(simpleMd["insecure-push"]) == "true" {
			ip = true
		}
		if string(simpleMd["export-dir"]) == "true" {
			expSrc := &exporter.Source{
				Ref:      ref,
				Refs:     map[string]cache.ImmutableRef{},
				Metadata: simpleMd,
			}
			dirExpSrcs = append(dirExpSrcs, expSrc)
		}
		if isImage {
			name := ""
			if n, ok := simpleMd["image.name"]; ok {
				name = string(n)
			}
			if name == "" {
				return nil, errors.Errorf("exporting image with no name")
			}
			imgNames, err := normalizedNames(name)
			if err != nil {
				return nil, err
			}
			if len(imgNames) == 0 {
				return nil, errors.Errorf("exporting image with no name")
			}
			delete(simpleMd, "image.name")

			platStr := string(simpleMd["platform"])
			if platStr != "" {
				p, err := platforms.Parse(platStr)
				if err != nil {
					return nil, errors.Wrap(err, "parse platform")
				}
				plat := exptypes.Platform{
					ID:       platStr,
					Platform: p,
				}
				for _, imgName := range imgNames {
					plats[imgName] = append(plats[imgName], plat)
				}
			}

			for _, imgName := range imgNames {
				le2, ok := localExport[imgName]
				if !ok {
					localExport[imgName] = le
					le2 = le
				}
				if le != le2 {
					return nil, errors.Errorf("inconsistent local-export/no-local-export setting for image %s", imgName)
				}

				sp2, ok := shouldPush[imgName]
				if !ok {
					shouldPush[imgName] = sp
					sp2 = sp
				}
				if sp != sp2 {
					return nil, errors.Errorf("inconsistent push/no-push setting for image %s", imgName)
				}

				ip2, ok := insecurePush[imgName]
				if !ok {
					insecurePush[imgName] = ip
					ip2 = ip
				}
				if ip != ip2 {
					return nil, errors.Errorf("inconsistent secure/insecure setting for image %s", imgName)
				}

				expSrc, ok := imageExpSrcs[imgName]
				if !ok {
					expSrc = &exporter.Source{
						Refs:     map[string]cache.ImmutableRef{},
						Metadata: make(map[string][]byte),
					}
					expSrc.Metadata["image.name"] = []byte(imgName)
					if le {
						expSrc.Metadata["export-image"] = []byte("true")
					}
					if sp {
						expSrc.Metadata["export-image-push"] = []byte("true")
					}
					if ip {
						expSrc.Metadata["insecure-push"] = []byte("true")
					}
					imageExpSrcs[imgName] = expSrc
				}
				if platStr != "" {
					expSrc.Refs[platStr] = ref
				} else {
					expSrc.Ref = ref
				}

				for mdK, mdV := range simpleMd {
					if platStr != "" {
						expSrc.Metadata[fmt.Sprintf("%s/%s", mdK, platStr)] = mdV
					} else {
						expSrc.Metadata[mdK] = mdV
					}
				}
			}
		}
	}
	for imgName, ps := range plats {
		if len(ps) > 0 {
			expPlats := &exptypes.Platforms{Platforms: ps}
			dt, err := json.Marshal(expPlats)
			if err != nil {
				return nil, err
			}
			imageExpSrcs[imgName].Metadata[exptypes.ExporterPlatformsKey] = dt
		}
	}

	ctx, done, err := leaseutil.WithLease(ctx, e.opt.LeaseManager, leaseutil.MakeTemporary)
	if err != nil {
		return nil, err
	}
	defer done(context.TODO())

	descs := make(map[string]*ocispec.Descriptor) // imgName -> ImageWriter.Commit desc
	for imgName, expSrc := range imageExpSrcs {
		desc, err := e.opt.ImageWriter.Commit(ctx, *expSrc, e.ociTypes, e.layerCompression, sessionID)
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
		descs[imgName] = desc
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.opt.SessionManager.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, err
	}

	resp := make(map[string]string)
	// TODO(vladaionescu): Fill resp

	writers := make(map[string]io.WriteCloser) // imgName -> writer
	for imgName, expSrc := range imageExpSrcs {
		md := make(map[string]string)
		for mdK, mdV := range expSrc.Metadata {
			md[safeGrpcMetaKey(mdK)] = string(mdV)
		}
		w, err := filesync.CopyFileWriter(ctx, md, caller)
		if err != nil {
			return nil, err
		}
		writers[imgName] = w
	}
	eg, egCtx := errgroup.WithContext(ctx)
	for _, expSrc := range dirExpSrcs {
		md := make(map[string]string)
		for mdK, mdV := range expSrc.Metadata {
			md[safeGrpcMetaKey(mdK)] = string(mdV)
		}
		eg.Go(exportDirFunc(egCtx, md, caller, expSrc.Ref, sessionID))
	}

	mproviders := make(map[string]*contentutil.MultiProvider) // imgName -> mprovider
	annotations := map[digest.Digest]map[string]string{}
	for imgName, expSrc := range imageExpSrcs {
		mprovider := contentutil.NewMultiProvider(e.opt.ImageWriter.ContentStore())
		mproviders[imgName] = mprovider
		for _, r := range expSrc.Refs {
			remote, err := r.GetRemote(ctx, false, e.layerCompression, session.NewGroup(sessionID))
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
		}
		if expSrc.Ref != nil { // This is a copy and paste of the above code
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

		}

		if shouldPush[imgName] {
			err := push.Push(
				ctx, e.opt.SessionManager, sessionID, mprovider,
				e.opt.ImageWriter.ContentStore(), descs[imgName].Digest,
				imgName, insecurePush[imgName], e.opt.RegistryHosts, false, annotations)
			if err != nil {
				return nil, err
			}
		}
	}

	report := oneOffProgress(ctx, "sending tarballs")
	for imgName, le := range localExport {
		if !le {
			continue
		}
		w := writers[imgName]
		desc := descs[imgName]
		expOpts := []archiveexporter.ExportOpt{archiveexporter.WithManifest(*desc, imgName)}
		switch e.opt.Variant {
		case VariantOCI:
			expOpts = append(expOpts, archiveexporter.WithAllPlatforms(), archiveexporter.WithSkipDockerManifest())
		case VariantDocker:
		default:
			return nil, report(errors.Errorf("invalid variant %q", e.opt.Variant))
		}
		if err := archiveexporter.Export(ctx, mproviders[imgName], w, expOpts...); err != nil {
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

func safeGrpcMetaKey(k string) string {
	return strings.ReplaceAll(k, "/", "-")
}
