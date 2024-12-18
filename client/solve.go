package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	contentlocal "github.com/containerd/containerd/v2/plugins/content/local"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/ociindex"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	sessioncontent "github.com/moby/buildkit/session/content"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/grpchijack"
	"github.com/moby/buildkit/solver/pb"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/util/bklog"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
)

type SolveOpt struct {
	Exports             []ExportEntry
	LocalDirs           map[string]string // Deprecated: use LocalMounts
	LocalMounts         map[string]fsutil.FS
	OCIStores           map[string]content.Store
	SharedKey           string
	Frontend            string
	FrontendAttrs       map[string]string
	FrontendInputs      map[string]llb.State
	CacheExports        []CacheOptionsEntry
	CacheImports        []CacheOptionsEntry
	Session             []session.Attachable
	AllowedEntitlements []string
	// When the session is custom-initialized, ParseExporterOpts need to be used to correctly
	// set up the session for export.
	SharedSession         *session.Session // TODO: refactor to better session syncing
	SessionPreInitialized bool             // TODO: refactor to better session syncing
	Internal              bool
	SourcePolicy          *spb.Policy
	Ref                   string

	// internal exporter state
	s solverState
}

type solverState struct {
	// storesToUpdate maps exporter ID -> oci store
	storesToUpdate map[string]ociStore
	cacheOpt       *cacheOptions
	// Only one of runGateway or def can be set.
	// runGateway optionally defines the gateway callback
	runGateway runGatewayCB
	// def optionally defines the LLB definition for the client
	def *llb.Definition
}

type cacheOptions struct {
	options        controlapi.CacheOptions
	contentStores  map[string]content.Store // key: ID of content store ("local:" + csDir)
	storesToUpdate map[string]string        // key: path to content store, value: tag
	frontendAttrs  map[string]string
}

type ociStore struct {
	path string
}

type ExportEntry struct {
	Type      string
	Attrs     map[string]string
	Output    filesync.FileOutputFunc // for ExporterOCI and ExporterDocker
	OutputDir string                  // for ExporterLocal
}

type CacheOptionsEntry struct {
	Type  string
	Attrs map[string]string
}

// Solve calls Solve on the controller.
// def must be nil if (and only if) opt.Frontend is set.
func (c *Client) Solve(ctx context.Context, def *llb.Definition, opt SolveOpt, statusChan chan *SolveStatus) (*SolveResponse, error) {
	defer func() {
		if statusChan != nil {
			close(statusChan)
		}
	}()

	if opt.Frontend == "" && def == nil {
		return nil, errors.New("invalid empty definition")
	}
	if opt.Frontend != "" && def != nil {
		return nil, errors.Errorf("invalid definition for frontend %s", opt.Frontend)
	}
	opt.s.def = def

	return c.solve(ctx, opt, statusChan)
}

type runGatewayCB func(ref string, s *session.Session, opts map[string]string) error

// ParseExporterOpts configures the specified session with the underlying exporter configuration.
// It needs to be invoked *after* ParseCacheOpts
func ParseExporterOpts(opt *SolveOpt, s *session.Session) error {
	mounts, err := prepareMounts(opt)
	if err != nil {
		return err
	}
	syncedDirs, err := prepareSyncedFiles(opt.s.def, mounts)
	if err != nil {
		return err
	}

	if len(syncedDirs) > 0 {
		s.Allow(filesync.NewFSSyncProvider(syncedDirs))
	}

	for _, a := range opt.Session {
		s.Allow(a)
	}

	contentStores := map[string]content.Store{}
	maps.Copy(contentStores, opt.s.cacheOpt.contentStores)
	for key, store := range opt.OCIStores {
		key2 := "oci:" + key
		if _, ok := contentStores[key2]; ok {
			return errors.Errorf("oci store key %q already exists", key)
		}
		contentStores[key2] = store
	}

	var syncTargets []filesync.FSSyncTarget
	for exID, ex := range opt.Exports {
		var supportFile bool
		var supportDir bool
		switch ex.Type {
		case ExporterLocal:
			supportDir = true
		case ExporterTar:
			supportFile = true
		case ExporterOCI, ExporterDocker:
			supportDir = ex.OutputDir != ""
			supportFile = ex.Output != nil
		}
		if supportFile && supportDir {
			return errors.Errorf("both file and directory output is not supported by %s exporter", ex.Type)
		}
		if !supportFile && ex.Output != nil {
			return errors.Errorf("output file writer is not supported by %s exporter", ex.Type)
		}
		if !supportDir && ex.OutputDir != "" {
			return errors.Errorf("output directory is not supported by %s exporter", ex.Type)
		}
		if supportFile {
			if ex.Output == nil {
				return errors.Errorf("output file writer is required for %s exporter", ex.Type)
			}
			syncTargets = append(syncTargets, filesync.WithFSSync(exID, ex.Output))
		}
		if supportDir {
			if ex.OutputDir == "" {
				return errors.Errorf("output directory is required for %s exporter", ex.Type)
			}
			switch ex.Type {
			case ExporterOCI, ExporterDocker:
				if err := os.MkdirAll(ex.OutputDir, 0755); err != nil {
					return err
				}
				cs, err := contentlocal.NewStore(ex.OutputDir)
				if err != nil {
					return err
				}
				contentStores["export"] = cs
				if opt.s.storesToUpdate == nil {
					opt.s.storesToUpdate = make(map[string]ociStore)
				}
				opt.s.storesToUpdate[strconv.Itoa(exID)] = ociStore{path: ex.OutputDir}
			default:
				syncTargets = append(syncTargets, filesync.WithFSSyncDir(exID, ex.OutputDir))
			}
		}
	}
	if len(contentStores) > 0 {
		s.Allow(sessioncontent.NewAttachable(contentStores))
	}

	if len(syncTargets) > 0 {
		s.Allow(filesync.NewFSSyncTarget(syncTargets...))
	}
	return nil
}

func (c *Client) solve(ctx context.Context, opt SolveOpt, statusChan chan *SolveStatus) (*SolveResponse, error) {
	if opt.s.def != nil && opt.s.runGateway != nil {
		return nil, errors.New("invalid with def and cb")
	}

	ref := identity.NewID()
	if opt.Ref != "" {
		ref = opt.Ref
	}
	eg, ctx := errgroup.WithContext(ctx)

	statusContext, cancelStatus := context.WithCancelCause(context.Background())
	defer cancelStatus(errors.WithStack(context.Canceled))

	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		statusContext = trace.ContextWithSpan(statusContext, span)
	}

	s := opt.SharedSession
	if s == nil {
		if opt.SessionPreInitialized {
			return nil, errors.Errorf("no session provided for preinitialized option")
		}
		var err error
		s, err = session.NewSession(statusContext, opt.SharedKey)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create session")
		}
	}

	err := ParseCacheOptions(ctx, &opt)
	if err != nil {
		return nil, err
	}

	if !opt.SessionPreInitialized {
		if err := ParseExporterOpts(&opt, s); err != nil {
			return nil, err
		}

		eg.Go(func() error {
			sd := c.sessionDialer
			if sd == nil {
				sd = grpchijack.Dialer(c.ControlClient())
			}
			return s.Run(statusContext, sd)
		})
	}

	frontendAttrs := maps.Clone(opt.FrontendAttrs)
	maps.Copy(frontendAttrs, opt.s.cacheOpt.frontendAttrs)

	solveCtx, cancelSolve := context.WithCancelCause(ctx)
	var res *SolveResponse
	eg.Go(func() error {
		ctx := solveCtx
		defer cancelSolve(errors.WithStack(context.Canceled))

		defer func() { // make sure the Status ends cleanly on build errors
			go func() {
				<-time.After(3 * time.Second)
				cancelStatus(errors.WithStack(context.Canceled))
			}()
			if !opt.SessionPreInitialized {
				bklog.G(ctx).Debugf("stopping session")
				s.Close()
			}
		}()
		var pbd *pb.Definition
		if opt.s.def != nil {
			pbd = opt.s.def.ToPB()
		}

		frontendInputs := make(map[string]*pb.Definition)
		for key, st := range opt.FrontendInputs {
			def, err := st.Marshal(ctx)
			if err != nil {
				return err
			}
			frontendInputs[key] = def.ToPB()
		}

		exports := make([]*controlapi.Exporter, 0, len(opt.Exports))
		exportDeprecated := ""
		exportAttrDeprecated := map[string]string{}
		for i, exp := range opt.Exports {
			if i == 0 {
				exportDeprecated = exp.Type
				exportAttrDeprecated = exp.Attrs
			}
			exports = append(exports, &controlapi.Exporter{
				Type:  exp.Type,
				Attrs: exp.Attrs,
				// Keep this in sync with SetupExporters id assignment
				ID: strconv.Itoa(i),
			})
		}

		resp, err := c.ControlClient().Solve(ctx, &controlapi.SolveRequest{
			Ref:                     ref,
			Definition:              pbd,
			Exporters:               exports,
			ExporterDeprecated:      exportDeprecated,
			ExporterAttrsDeprecated: exportAttrDeprecated,
			Session:                 s.ID(),
			Frontend:                opt.Frontend,
			FrontendAttrs:           frontendAttrs,
			FrontendInputs:          frontendInputs,
			Cache:                   &opt.s.cacheOpt.options,
			Entitlements:            slices.Clone(opt.AllowedEntitlements),
			Internal:                opt.Internal,
			SourcePolicy:            opt.SourcePolicy,
		})
		if err != nil {
			return errors.Wrap(err, "failed to solve")
		}
		res = &SolveResponse{
			ExporterResponse:       resp.ExporterResponseDeprecated,
			ExporterResponses:      make([]ExporterResponse, 0, len(resp.ExporterResponses)),
			CacheExporterResponses: make([]ExporterResponse, 0, len(resp.CacheExporterResponses)),
		}
		for _, resp := range resp.ExporterResponses {
			res.ExporterResponses = append(res.ExporterResponses, ExporterResponse{
				ID:   resp.Metadata.ID,
				Data: resp.Data,
			})
		}
		for _, resp := range resp.CacheExporterResponses {
			res.CacheExporterResponses = append(res.CacheExporterResponses, ExporterResponse{
				ID:   resp.Metadata.ID,
				Type: resp.Metadata.Type,
				Data: resp.Data,
			})
		}
		return nil
	})

	if opt.s.runGateway != nil {
		eg.Go(func() error {
			err := opt.s.runGateway(ref, s, frontendAttrs)
			if err == nil {
				return nil
			}

			// If the callback failed then the main
			// `Solve` (called above) should error as
			// well. However as a fallback we wait up to
			// 5s for that to happen before failing this
			// goroutine.
			select {
			case <-solveCtx.Done():
			case <-time.After(5 * time.Second):
				cancelSolve(errors.WithStack(context.Canceled))
			}

			return err
		})
	}

	eg.Go(func() error {
		stream, err := c.ControlClient().Status(statusContext, &controlapi.StatusRequest{
			Ref: ref,
		})
		if err != nil {
			return errors.Wrap(err, "failed to get status")
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return errors.Wrap(err, "failed to receive status")
			}
			if statusChan != nil {
				statusChan <- NewSolveStatus(resp)
			}
		}
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}
	// Update index.json of exported cache content store
	// FIXME(AkihiroSuda): dedupe const definition of cache/remotecache.ExporterResponseManifestDesc = "cache.manifest"
	if manifestDescJSON := res.ExporterResponse["cache.manifest"]; manifestDescJSON != "" {
		var manifestDesc ocispecs.Descriptor
		if err = json.Unmarshal([]byte(manifestDescJSON), &manifestDesc); err != nil {
			return nil, err
		}
		for storePath, tag := range opt.s.cacheOpt.storesToUpdate {
			idx := ociindex.NewStoreIndex(storePath)
			if err := idx.Put(manifestDesc, ociindex.Tag(tag)); err != nil {
				return nil, err
			}
		}
	}

	if len(opt.s.storesToUpdate) == 0 {
		return res, nil
	}
	for id, store := range opt.s.storesToUpdate {
		manifestDesc, err := getManifestDescriptor(id, res)
		if err != nil {
			return nil, err
		}
		if manifestDesc == nil {
			continue
		}
		names := []ociindex.NameOrTag{ociindex.Tag("latest")}
		if resp := res.exporter(id); resp != nil {
			if t, ok := resp.Data["image.name"]; ok {
				inp := strings.Split(t, ",")
				names = make([]ociindex.NameOrTag, len(inp))
				for i, n := range inp {
					names[i] = ociindex.Name(n)
				}
			}
		}
		idx := ociindex.NewStoreIndex(store.path)
		if err := idx.Put(*manifestDesc, names...); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func getManifestDescriptor(exporterID string, resp *SolveResponse) (*ocispecs.Descriptor, error) {
	if resp := resp.exporter(exporterID); resp != nil {
		if manifestDescDt := resp.Data[exptypes.ExporterImageDescriptorKey]; manifestDescDt != "" {
			return unmarshalManifestDescriptor(manifestDescDt)
		}
	}
	if manifestDescDt := resp.ExporterResponse[exptypes.ExporterImageDescriptorKey]; manifestDescDt != "" {
		return unmarshalManifestDescriptor(manifestDescDt)
	}
	return nil, nil
}

func unmarshalManifestDescriptor(manifestDesc string) (*ocispecs.Descriptor, error) {
	manifestDescDt, err := base64.StdEncoding.DecodeString(manifestDesc)
	if err != nil {
		return nil, err
	}
	var desc ocispecs.Descriptor
	if err = json.Unmarshal([]byte(manifestDescDt), &desc); err != nil {
		return nil, err
	}
	return &desc, nil
}

func prepareSyncedFiles(def *llb.Definition, localMounts map[string]fsutil.FS) (filesync.StaticDirSource, error) {
	resetUIDAndGID := func(p string, st *fstypes.Stat) fsutil.MapResult {
		st.Uid = 0
		st.Gid = 0
		return fsutil.MapResultKeep
	}

	result := make(filesync.StaticDirSource, len(localMounts))
	if def == nil {
		for name, mount := range localMounts {
			mount, err := fsutil.NewFilterFS(mount, &fsutil.FilterOpt{
				Map: resetUIDAndGID,
			})
			if err != nil {
				return nil, err
			}
			result[name] = mount
		}
	} else {
		for _, dt := range def.Def {
			var op pb.Op
			if err := op.UnmarshalVT(dt); err != nil {
				return nil, errors.Wrap(err, "failed to parse llb proto op")
			}
			if src := op.GetSource(); src != nil {
				if strings.HasPrefix(src.Identifier, "local://") {
					name := strings.TrimPrefix(src.Identifier, "local://")
					mount, ok := localMounts[name]
					if !ok {
						return nil, errors.Errorf("local directory %s not enabled", name)
					}
					mount, err := fsutil.NewFilterFS(mount, &fsutil.FilterOpt{
						Map: resetUIDAndGID,
					})
					if err != nil {
						return nil, err
					}
					result[name] = mount
				}
			}
		}
	}
	return result, nil
}

func ParseCacheOptions(ctx context.Context, opt *SolveOpt) error {
	var (
		cacheExports []*controlapi.CacheOptionsEntry
		cacheImports []*controlapi.CacheOptionsEntry
	)
	contentStores := make(map[string]content.Store)
	storesToUpdate := make(map[string]string)
	frontendAttrs := make(map[string]string)
	for _, ex := range opt.CacheExports {
		if ex.Type == "local" {
			csDir := ex.Attrs["dest"]
			if csDir == "" {
				return errors.New("local cache exporter requires dest")
			}
			if err := os.MkdirAll(csDir, 0755); err != nil {
				return err
			}
			cs, err := contentlocal.NewStore(csDir)
			if err != nil {
				return err
			}
			contentStores["local:"+csDir] = cs

			tag := "latest"
			if t, ok := ex.Attrs["tag"]; ok {
				tag = t
			}
			// TODO(AkihiroSuda): support custom index JSON path and tag
			storesToUpdate[csDir] = tag
		}
		if ex.Type == "registry" {
			regRef := ex.Attrs["ref"]
			if regRef == "" {
				return errors.New("registry cache exporter requires ref")
			}
		}
		cacheExports = append(cacheExports, &controlapi.CacheOptionsEntry{
			Type:  ex.Type,
			Attrs: ex.Attrs,
		})
	}
	for _, im := range opt.CacheImports {
		if im.Type == "local" {
			csDir := im.Attrs["src"]
			if csDir == "" {
				return errors.New("local cache importer requires src")
			}
			cs, err := contentlocal.NewStore(csDir)
			if err != nil {
				bklog.G(ctx).Warning("local cache import at " + csDir + " not found due to err: " + err.Error())
				continue
			}
			// if digest is not specified, attempt to load from tag
			if im.Attrs["digest"] == "" {
				tag := "latest"
				if t, ok := im.Attrs["tag"]; ok {
					tag = t
				}

				idx := ociindex.NewStoreIndex(csDir)
				desc, err := idx.Get(tag)
				if err != nil {
					bklog.G(ctx).Warning("local cache import at " + csDir + " not found due to err: " + err.Error())
					continue
				}
				if desc != nil {
					im.Attrs["digest"] = desc.Digest.String()
				}
			}
			if im.Attrs["digest"] == "" {
				return errors.New("local cache importer requires either explicit digest, \"latest\" tag or custom tag on index.json")
			}
			contentStores["local:"+csDir] = cs
		}
		if im.Type == "registry" {
			regRef := im.Attrs["ref"]
			if regRef == "" {
				return errors.New("registry cache importer requires ref")
			}
		}
		cacheImports = append(cacheImports, &controlapi.CacheOptionsEntry{
			Type:  im.Type,
			Attrs: im.Attrs,
		})
	}
	if opt.Frontend != "" || opt.s.runGateway != nil {
		if len(cacheImports) > 0 {
			s, err := json.Marshal(cacheImports)
			if err != nil {
				return err
			}
			frontendAttrs["cache-imports"] = string(s)
		}
	}
	opt.s.cacheOpt = &cacheOptions{
		options: controlapi.CacheOptions{
			Exports: cacheExports,
			Imports: cacheImports,
		},
		contentStores:  contentStores,
		storesToUpdate: storesToUpdate,
		frontendAttrs:  frontendAttrs,
	}
	return nil
}

func prepareMounts(opt *SolveOpt) (map[string]fsutil.FS, error) {
	// merge local mounts and fallback local directories together
	mounts := make(map[string]fsutil.FS)
	for k, mount := range opt.LocalMounts {
		mounts[k] = mount
	}
	for k, dir := range opt.LocalDirs {
		mount, err := fsutil.NewFS(dir)
		if err != nil {
			return nil, err
		}
		if _, ok := mounts[k]; ok {
			return nil, errors.Errorf("local mount %s already exists", k)
		}
		mounts[k] = mount
	}
	return mounts, nil
}
