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
	Exports               []ExportEntry
	EnableSessionExporter bool
	LocalDirs             map[string]string // Deprecated: use LocalMounts
	LocalMounts           map[string]fsutil.FS
	OCIStores             map[string]content.Store
	SharedKey             string
	Frontend              string
	FrontendAttrs         map[string]string
	FrontendInputs        map[string]llb.State
	CacheExports          []CacheOptionsEntry
	CacheImports          []CacheOptionsEntry
	Session               []session.Attachable
	AllowedEntitlements   []string
	// When the session is custom-initialized, Init can be used to
	// set up the session for export automatically.
	SharedSession         *session.Session // TODO: refactor to better session syncing
	SessionPreInitialized bool             // TODO: refactor to better session syncing
	Internal              bool
	SourcePolicy          *spb.Policy
	SourcePolicyProvider  session.Attachable
	Ref                   string

	// internal solver state
	s solverState
}

type solverState struct {
	exporterOpt *exporterOptions
	cacheOpt    *cacheOptions
	// Only one of runGateway or def can be set.
	// runGateway optionally defines the gateway callback
	runGateway runGatewayCB
	// def optionally defines the LLB definition for the client
	def *llb.Definition
}

type exporterOptions struct {
	// storesToUpdate maps exporter ID -> oci store
	storesToUpdate map[string]ociStore
}

type cacheOptions struct {
	options        controlapi.CacheOptions
	contentStores  map[string]content.Store // key: ID of content store ("local:" + csDir)
	storesToUpdate map[string]ociStore      // key: exporter ID
	frontendAttrs  map[string]string
}

type ociStore struct {
	path string
	tag  string
}

type ExportEntry struct {
	Type        string
	Attrs       map[string]string
	Output      filesync.FileOutputFunc // for ExporterOCI and ExporterDocker
	OutputDir   string                  // for ExporterLocal
	OutputStore content.Store

	// id identifies the exporter in the configuration.
	// Will be assigned automatically and should not be set by the user.
	id string
}

type CacheOptionsEntry struct {
	Type  string
	Attrs map[string]string

	// id identifies the exporter in the configuration.
	// Will be assigned automatically and should not be set by the user.
	id string
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

// Init initializes the SolveOpt.
// It parses and initializes the cache exports/imports and output exporters.
func (opt *SolveOpt) Init(ctx context.Context, s *session.Session) error {
	opt.initExporterIDs()
	if err := opt.parseCacheOptions(ctx); err != nil {
		return err
	}
	return opt.parseExporterOptions(s)
}

func (opt *SolveOpt) initExporterIDs() {
	for i := range opt.Exports {
		opt.Exports[i].id = strconv.Itoa(i)
	}
	for i := range opt.CacheExports {
		opt.CacheExports[i].id = strconv.Itoa(i)
	}
}

// parseExporterOptions configures the specified session with the underlying exporter configuration.
// It needs to be invoked *after* ParseCacheOpts
func (opt *SolveOpt) parseExporterOptions(s *session.Session) error {
	if opt.s.exporterOpt != nil {
		return nil
	}

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

	opt.s.exporterOpt = &exporterOptions{}
	var syncTargets []filesync.FSSyncTarget
	for _, ex := range opt.Exports {
		var supportFile, supportDir, supportStore bool
		switch ex.Type {
		case ExporterLocal:
			supportDir = true
		case ExporterTar:
			supportFile = true
		case ExporterOCI, ExporterDocker:
			supportFile = ex.Output != nil
			supportStore = ex.OutputStore != nil || ex.OutputDir != ""
			if supportFile && supportStore {
				return errors.Errorf("both file and store output is not supported by %s exporter", ex.Type)
			}
		}
		if !supportFile && ex.Output != nil {
			return errors.Errorf("output file writer is not supported by %s exporter", ex.Type)
		}
		if !supportDir && !supportStore && ex.OutputDir != "" {
			return errors.Errorf("output directory is not supported by %s exporter", ex.Type)
		}
		if !supportStore && ex.OutputStore != nil {
			return errors.Errorf("output store is not supported by %s exporter", ex.Type)
		}
		if supportFile {
			if ex.Output == nil {
				return errors.Errorf("output file writer is required for %s exporter", ex.Type)
			}
			syncTargets = append(syncTargets, filesync.WithFSSync(ex.id, ex.Output))
		}
		if supportDir {
			if ex.OutputDir == "" {
				return errors.Errorf("output directory is required for %s exporter", ex.Type)
			}
			syncTargets = append(syncTargets, filesync.WithFSSyncDir(ex.id, ex.OutputDir))
		}
		if supportStore {
			store := ex.OutputStore
			if store == nil {
				if err := os.MkdirAll(ex.OutputDir, 0755); err != nil {
					return err
				}
				store, err = contentlocal.NewStore(ex.OutputDir)
				if err != nil {
					return err
				}
				if opt.s.exporterOpt.storesToUpdate == nil {
					opt.s.exporterOpt.storesToUpdate = make(map[string]ociStore)
				}
				opt.s.exporterOpt.storesToUpdate[ex.id] = ociStore{path: ex.OutputDir}
			}

			// TODO: this should be dependent on the exporter id (to allow multiple oci exporters)
			storeName := "export"
			if _, ok := contentStores[storeName]; ok {
				return errors.Errorf("oci store key %q already exists", storeName)
			}
			contentStores[storeName] = store
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

	opt.initExporterIDs()

	err := opt.parseCacheOptions(ctx)
	if err != nil {
		return nil, err
	}

	if !opt.SessionPreInitialized {
		if err := opt.parseExporterOptions(s); err != nil {
			return nil, err
		}

		if opt.SourcePolicyProvider != nil {
			s.Allow(opt.SourcePolicyProvider)
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

	const statusInactivityTimeout = 5 * time.Second
	statusActivity := make(chan struct{}, 1)

	solveCtx, cancelSolve := context.WithCancelCause(ctx)
	var res *SolveResponse
	eg.Go(func() error {
		ctx := solveCtx
		defer cancelSolve(errors.WithStack(context.Canceled))

		defer func() { // make sure the Status ends cleanly on build errors
			go func() {
				// Start inactivity monitoring after solve completes
				statusInactivityTimer := time.NewTimer(statusInactivityTimeout)
				defer statusInactivityTimer.Stop()
				for {
					select {
					case <-statusContext.Done():
						return
					case <-statusActivity:
						// Reset timer on activity
						statusInactivityTimer.Reset(statusInactivityTimeout)
					case <-statusInactivityTimer.C:
						cancelStatus(errors.WithStack(context.Canceled))
						return
					}
				}
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
				ID:    exp.id,
			})
		}

		sopt := &controlapi.SolveRequest{
			Ref:                     ref,
			Definition:              pbd,
			Exporters:               exports,
			ExporterDeprecated:      exportDeprecated,
			ExporterAttrsDeprecated: exportAttrDeprecated,
			EnableSessionExporter:   opt.EnableSessionExporter,
			Session:                 s.ID(),
			Frontend:                opt.Frontend,
			FrontendAttrs:           frontendAttrs,
			FrontendInputs:          frontendInputs,
			Cache:                   &opt.s.cacheOpt.options,
			Entitlements:            slices.Clone(opt.AllowedEntitlements),
			Internal:                opt.Internal,
			SourcePolicy:            opt.SourcePolicy,
		}
		if opt.SourcePolicyProvider != nil {
			sopt.SourcePolicySession = s.ID()
		}

		resp, err := c.ControlClient().Solve(ctx, sopt)
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
				Type: resp.Metadata.Type,
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
				if errors.Is(err, io.EOF) {
					return nil
				}
				// Ignore context canceled, triggered after inactivity timeout
				if errors.Is(err, context.Canceled) || statusContext.Err() != nil {
					return nil
				}
				return errors.Wrap(err, "failed to receive status")
			}
			// Signal activity (non-blocking)
			select {
			case statusActivity <- struct{}{}:
			default:
			}
			if statusChan != nil {
				statusChan <- NewSolveStatus(resp)
			}
		}
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	for id, store := range opt.s.cacheOpt.storesToUpdate {
		// Update index.json of exported cache content store
		manifestDesc, err := getCacheManifestDescriptor(id, res)
		if err != nil {
			return nil, err
		}
		if manifestDesc == nil {
			continue
		}
		idx := ociindex.NewStoreIndex(store.path)
		if err := idx.Put(*manifestDesc, ociindex.Tag(store.tag)); err != nil {
			return nil, err
		}
	}

	if len(opt.s.exporterOpt.storesToUpdate) == 0 {
		return res, nil
	}
	for id, store := range opt.s.exporterOpt.storesToUpdate {
		manifestDesc, err := getImageManifestDescriptor(id, res)
		if err != nil {
			return nil, err
		}
		if manifestDesc == nil {
			continue
		}
		names := []ociindex.NameOrTag{ociindex.Tag("latest")}
		if resp := res.exporter(id); resp != nil {
			if t, ok := resp.Data[exptypes.ExporterImageNameKey]; ok {
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

func getCacheManifestDescriptor(exporterID string, resp *SolveResponse) (*ocispecs.Descriptor, error) {
	const exporterResponseManifestDesc = "cache.manifest"
	if resp := resp.cacheExporter(exporterID); resp != nil {
		// FIXME(AkihiroSuda): dedupe const definition of cache/remotecache.ExporterResponseManifestDesc = "cache.manifest"
		if manifestDescDt := resp.Data[exporterResponseManifestDesc]; manifestDescDt != "" {
			return unmarshalManifestDescriptor(manifestDescDt)
		}
	}
	if manifestDescDt := resp.ExporterResponse[exporterResponseManifestDesc]; manifestDescDt != "" {
		return unmarshalManifestDescriptor(manifestDescDt)
	}
	return nil, nil
}

func getImageManifestDescriptor(exporterID string, resp *SolveResponse) (*ocispecs.Descriptor, error) {
	if resp := resp.exporter(exporterID); resp != nil {
		if manifestDescDt := resp.Data[exptypes.ExporterImageDescriptorKey]; manifestDescDt != "" {
			return unmarshalEncodedManifestDescriptor(manifestDescDt)
		}
	}
	if manifestDescDt := resp.ExporterResponse[exptypes.ExporterImageDescriptorKey]; manifestDescDt != "" {
		return unmarshalEncodedManifestDescriptor(manifestDescDt)
	}
	return nil, nil
}

func unmarshalEncodedManifestDescriptor(base64Payload string) (*ocispecs.Descriptor, error) {
	manifestDescDt, err := base64.StdEncoding.DecodeString(base64Payload)
	if err != nil {
		return nil, err
	}
	return unmarshalManifestDescriptor(string(manifestDescDt))
}

func unmarshalManifestDescriptor(manifestDescJSON string) (*ocispecs.Descriptor, error) {
	var desc ocispecs.Descriptor
	if err := json.Unmarshal([]byte(manifestDescJSON), &desc); err != nil {
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
				if name, ok := strings.CutPrefix(src.Identifier, "local://"); ok {
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

func (opt *SolveOpt) parseCacheOptions(ctx context.Context) error {
	if opt.s.cacheOpt != nil {
		return nil
	}
	var (
		cacheExports []*controlapi.CacheOptionsEntry
		cacheImports []*controlapi.CacheOptionsEntry
	)
	contentStores := make(map[string]content.Store)
	storesToUpdate := make(map[string]ociStore)
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
			storesToUpdate[ex.id] = ociStore{path: csDir, tag: tag}
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
			ID:    ex.id,
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
	maps.Copy(mounts, opt.LocalMounts)
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
