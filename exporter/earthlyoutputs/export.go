package earthlyoutputs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"

	"github.com/moby/buildkit/cache"
	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/exporter/earthlyoutputs/registry/eodriver"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/pullping"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/push"
)

type ExporterVariant string

const (
	keyPush           = "push"
	keyPushByDigest   = "push-by-digest"
	keyInsecure       = "registry.insecure"
	keyUnpack         = "unpack"
	keyDanglingPrefix = "dangling-name-prefix"
	keyNameCanonical  = "name-canonical"
	keyStore          = "store"

	// keyUnsafeInternalStoreAllowIncomplete should only be used for tests. This option allows exporting image to the image store
	// as well as lacking some blobs in the content store. Some integration tests for lazyref behaviour depends on this option.
	// Ignored when store=false.
	keyUnsafeInternalStoreAllowIncomplete = "unsafe-internal-store-allow-incomplete"
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
	i := &imageExporterInstance{
		imageExporter: e,
		opts: containerimage.ImageCommitOpts{
			RefCfg: cacheconfig.RefConfig{
				Compression: compression.New(compression.Default),
			},
			BuildInfo:   true,
			Annotations: make(containerimage.AnnotationsGroup),
		},
		store: true,
	}

	opt, err := i.opts.Load(opt)
	if err != nil {
		return nil, err
	}

	for k, v := range opt {
		switch k {
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
		case keyPushByDigest:
			if v == "" {
				i.pushByDigest = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.pushByDigest = b
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
		case keyUnpack:
			if v == "" {
				i.unpack = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.unpack = b
		case keyStore:
			if v == "" {
				i.store = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.store = b
		case keyUnsafeInternalStoreAllowIncomplete:
			if v == "" {
				i.storeAllowIncomplete = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.storeAllowIncomplete = b
		case keyDanglingPrefix:
			i.danglingPrefix = v
		case keyNameCanonical:
			if v == "" {
				i.nameCanonical = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			i.nameCanonical = b
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
	opts                 containerimage.ImageCommitOpts
	push                 bool
	pushByDigest         bool
	unpack               bool
	store                bool
	storeAllowIncomplete bool
	insecure             bool
	nameCanonical        bool
	danglingPrefix       string
	meta                 map[string][]byte
}

func (e *imageExporterInstance) Name() string {
	return "[output] exporting outputs"
}

type imgData struct {
	// localExport represents whether the image should be exported locally as a tar.
	localExport bool
	// localRegExport is set when the image should be exported via local registry. The value
	// represents the image name that can be used to pull the image from the local registry.
	localRegExport string

	// shouldPush is set when the image should be pushed.
	shouldPush bool
	// insecurePush is set when the push should take place over an unencrypted connection.
	insecurePush bool

	// mp is the MultiProvider for this image. Note that for local reg exports, the
	// mmp is used instead.
	mp *contentutil.MultiProvider

	// platforms is a list of platforms to build for.
	platforms []exptypes.Platform

	// expSrc is the exporter source constructed in a way that makes it look like this
	// image is the only one being exported.
	expSrc *exporter.Source

	// mfstDesc is the image manifest descriptor.
	mfstDesc *ocispecs.Descriptor

	// tarWriter is the image tar writer (set only if localExport is true).
	tarWriter io.WriteCloser

	// localRegExportReport is the one-off progress reporter for the local reg export.
	localRegExportReport func()
	// localExportReport is the one-off progress reporter for the tar export.
	localExportReport func()

	opts containerimage.ImageCommitOpts
}

func (e *imageExporterInstance) Export(ctx context.Context, src *exporter.Source, sessionID string) (map[string]string, exporter.DescriptorReference, error) {
	if src.Ref != nil {
		return nil, nil, errors.Errorf("export with src.Ref not supported")
	}
	if src.Metadata == nil {
		src.Metadata = make(map[string][]byte)
	}
	for k, v := range e.meta {
		src.Metadata[k] = v
	}
	images := make(map[string]*imgData)
	hasAnyTarExport := false
	hasAnyLocalRegExport := false
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

		opts := e.opts
		as, _, err := containerimage.ParseAnnotations(simpleMd)
		if err != nil {
			return nil, nil, err
		}
		opts.Annotations = as.Merge(opts.Annotations)

		le := false
		isImage := false
		if string(simpleMd["export-image"]) == "true" {
			isImage = true
			le = true
			hasAnyTarExport = true
		}
		eilr := ""
		if string(simpleMd["export-image-local-registry"]) != "" {
			isImage = true
			eilr = string(simpleMd["export-image-local-registry"])
			hasAnyLocalRegExport = true
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
				return nil, nil, errors.Errorf("exporting image with no name")
			}
			imgNames, err := normalizedNames(name)
			if err != nil {
				return nil, nil, err
			}
			if len(imgNames) == 0 {
				return nil, nil, errors.Errorf("exporting image with no name")
			}
			delete(simpleMd, "image.name")
			platStr := string(simpleMd["platform"])
			for _, imgName := range imgNames {
				img, ok := images[imgName]
				if !ok {
					img = &imgData{
						expSrc: &exporter.Source{
							Refs:     map[string]cache.ImmutableRef{},
							Metadata: make(map[string][]byte),
						},
					}
					images[imgName] = img
				}
				img.localExport = img.localExport || le
				img.localRegExport = eilr
				img.shouldPush = img.shouldPush || sp
				img.insecurePush = img.insecurePush || ip
				img.opts = opts

				img.expSrc.Metadata["image.name"] = []byte(imgName)
				if eilr != "" {
					img.expSrc.Metadata["export-image-local-registry"] = []byte(eilr)
				}
				if le {
					img.expSrc.Metadata["export-image"] = []byte("true")
				}
				if sp {
					img.expSrc.Metadata["export-image-push"] = []byte("true")
				}
				if ip {
					img.expSrc.Metadata["insecure-push"] = []byte("true")
				}
				if platStr != "" {
					img.expSrc.Refs[platStr] = ref

					p, err := platforms.Parse(platStr)
					if err != nil {
						return nil, nil, errors.Wrap(err, "parse platform")
					}
					plat := exptypes.Platform{
						ID:       platStr,
						Platform: p,
					}
					img.platforms = append(img.platforms, plat)
				} else {
					img.expSrc.Ref = ref
				}

				for mdK, mdV := range simpleMd {
					if platStr != "" {
						img.expSrc.Metadata[fmt.Sprintf("%s/%s", mdK, platStr)] = mdV
					} else {
						img.expSrc.Metadata[mdK] = mdV
					}
				}
			}
		}
	}
	for _, img := range images {
		if len(img.platforms) > 0 {
			expPlats := &exptypes.Platforms{Platforms: img.platforms}
			dt, err := json.Marshal(expPlats)
			if err != nil {
				return nil, nil, err
			}
			img.expSrc.Metadata[exptypes.ExporterPlatformsKey] = dt
		}
	}

	ctx, done, err := leaseutil.WithLease(ctx, e.opt.LeaseManager, leaseutil.MakeTemporary)
	if err != nil {
		return nil, nil, err
	}
	defer done(context.TODO())

	resp := make(map[string]string)
	for imgName, img := range images {
		desc, err := e.opt.ImageWriter.Commit(ctx, img.expSrc, sessionID, &img.opts)
		if err != nil {
			return nil, nil, err
		}
		img.mfstDesc = desc
		defer func() {
			e.opt.ImageWriter.ContentStore().Delete(context.TODO(), desc.Digest)
		}()
		if desc.Annotations == nil {
			desc.Annotations = map[string]string{}
		}
		desc.Annotations[ocispecs.AnnotationCreated] = time.Now().UTC().Format(time.RFC3339)

		if v, ok := desc.Annotations[exptypes.ExporterConfigDigestKey]; ok {
			cfgDgstKey := fmt.Sprintf("%s|%s", imgName, exptypes.ExporterConfigDigestKey)
			resp[cfgDgstKey] = v
			delete(desc.Annotations, exptypes.ExporterConfigDigestKey)
		}
		dtDesc, err := json.Marshal(desc)
		if err != nil {
			return nil, nil, err
		}
		descKey := fmt.Sprintf("%s|%s", imgName, exptypes.ExporterImageDescriptorKey)
		resp[descKey] = base64.StdEncoding.EncodeToString(dtDesc)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	caller, err := e.opt.SessionManager.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, nil, err
	}

	for _, img := range images {
		if !img.localExport {
			continue
		}
		md := make(map[string]string)
		for mdK, mdV := range img.expSrc.Metadata {
			md[safeGrpcMetaKey(mdK)] = string(mdV)
		}
		img.tarWriter, err = filesync.CopyFileWriter(ctx, md, caller)
		if err != nil {
			return nil, nil, err
		}
	}
	dirEG, egCtx := errgroup.WithContext(ctx)
	for _, expSrc := range dirExpSrcs {
		md := make(map[string]string)
		for mdK, mdV := range expSrc.Metadata {
			md[safeGrpcMetaKey(mdK)] = string(mdV)
		}
		dirEG.Go(exportDirFunc(egCtx, md, caller, expSrc.Ref, sessionID))
	}

	mmp := eodriver.MultiMultiProviderSingleton
	annotations := map[digest.Digest]map[string]string{}
	for imgName, img := range images {
		if img.localExport {
			img.localExportReport = oneOffProgress(ctx, fmt.Sprintf("transferring (via tar) %s", imgName))
		}
		if img.localRegExport != "" {
			img.localRegExportReport = oneOffProgress(ctx, fmt.Sprintf("transferring %s", imgName))

			err := mmp.AddImg(ctx, img.localRegExport, e.opt.ImageWriter.ContentStore(), img.mfstDesc.Digest)
			if err != nil {
				return nil, nil, err
			}
		}
		if img.localExport || img.shouldPush {
			img.mp = contentutil.NewMultiProvider(e.opt.ImageWriter.ContentStore())
		}

		for _, r := range img.expSrc.Refs {
			remotes, err := r.GetRemotes(ctx, false, e.opts.RefCfg, false, session.NewGroup(sessionID))
			if err != nil {
				return nil, nil, err
			}
			remote := remotes[0]
			// unlazy before export as some consumers do not handle
			// layer blobs in parallel (whereas unlazy does)
			if unlazier, ok := remote.Provider.(cache.Unlazier); ok {
				if err := unlazier.Unlazy(ctx); err != nil {
					return nil, nil, err
				}
			}
			for _, desc := range remote.Descriptors {
				if img.localRegExport != "" {
					err := mmp.AddImgSub(img.localRegExport, desc.Digest, remote.Provider)
					if err != nil {
						return nil, nil, err
					}
				}
				if img.localExport || img.shouldPush {
					img.mp.Add(desc.Digest, remote.Provider)
				}
				addAnnotations(annotations, desc)
			}
		}
		if img.expSrc.Ref != nil { // This is a copy and paste of the above code
			remotes, err := img.expSrc.Ref.GetRemotes(ctx, false, e.opts.RefCfg, false, session.NewGroup(sessionID))
			if err != nil {
				return nil, nil, err
			}
			remote := remotes[0]
			// unlazy before export as some consumers do not handle
			// layer blobs in parallel (whereas unlazy does)
			if unlazier, ok := remote.Provider.(cache.Unlazier); ok {
				if err := unlazier.Unlazy(ctx); err != nil {
					return nil, nil, err
				}
			}
			for _, desc := range remote.Descriptors {
				if img.localRegExport != "" {
					err := mmp.AddImgSub(img.localRegExport, desc.Digest, remote.Provider)
					if err != nil {
						return nil, nil, err
					}
				}
				if img.localExport || img.shouldPush {
					img.mp.Add(desc.Digest, remote.Provider)
				}
				addAnnotations(annotations, desc)
			}
		}

		if img.shouldPush {
			err := push.Push(
				ctx, e.opt.SessionManager, sessionID, img.mp,
				e.opt.ImageWriter.ContentStore(), img.mfstDesc.Digest,
				imgName, img.insecurePush, e.opt.RegistryHosts, false, annotations)
			if err != nil {
				return nil, nil, err
			}
		}
	}

	var pullPingChan chan error
	if hasAnyLocalRegExport {
		// inform the client that it's safe to perform pulls now
		pullImgs := make([]string, 0, len(images))
		for _, img := range images {
			if img.localRegExport != "" {
				pullImgs = append(pullImgs, img.localRegExport)
			}
		}
		pullPingChan = pullping.PullPingChannel(ctx, pullImgs, resp, caller)
		// wait for the client to finish pulling
		select {
		case err := <-pullPingChan:
			if err != nil {
				return nil, nil, errors.Wrap(err, "pull ping error")
			}
		case <-caller.Context().Done():
			return nil, nil, errors.Wrap(caller.Context().Err(), "caller context done")
		}
		for _, img := range images {
			if img.localRegExport != "" {
				img.localRegExportReport()
			}
		}
	}

	if hasAnyTarExport {
		for imgName, img := range images {
			if !img.localExport {
				continue
			}
			expOpts := []archiveexporter.ExportOpt{archiveexporter.WithManifest(*img.mfstDesc, imgName)}
			if err := archiveexporter.Export(ctx, img.mp, img.tarWriter, expOpts...); err != nil {
				img.tarWriter.Close()
				if grpcerrors.Code(err) == codes.AlreadyExists {
					continue
				}
				if errors.Is(err, io.EOF) {
					// TODO(vladaionescu): This sometimes happens when server responds with
					//                     GRPC code codes.AlreadyExists and
					//                     we continue to try to send data.
					continue
				}
				return nil, nil, err
			}
			err = img.tarWriter.Close()
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
				return nil, nil, err
			}
			img.localExportReport()
		}
		// just in case any report was skipped due to a continue
		for _, img := range images {
			if img.localExportReport != nil {
				img.localExportReport()
			}
		}
	}

	if err := dirEG.Wait(); err != nil {
		return nil, nil, err
	}

	// TODO DescriptorReference
	return resp, nil, nil
}

func (e *imageExporterInstance) Config() *exporter.Config {
	return exporter.NewConfigWithCompression(e.opts.RefCfg.Compression)
}

func oneOffProgress(ctx context.Context, id string) func() {
	pw, _, _ := progress.FromContext(ctx)(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func() {
		if st.Completed != nil {
			// Don't close twice.
			return
		}
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
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
	pw, _, _ := progress.FromContext(ctx)(ctx)
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
			src, err = os.MkdirTemp("", "buildkit")
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
			walkOpt.Map = func(p string, st *fstypes.Stat) fsutil.MapResult {
				uid, gid, err := idmap.ToContainer(idtools.Identity{
					UID: int(st.Uid),
					GID: int(st.Gid),
				})
				if err != nil {
					return fsutil.MapResultExclude
				}
				st.Uid = uint32(uid)
				st.Gid = uint32(gid)
				return fsutil.MapResultKeep
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

func addAnnotations(m map[digest.Digest]map[string]string, desc ocispecs.Descriptor) {
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
