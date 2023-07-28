package builder

import (
	"context"
	"strings"
	"sync"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/image"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/attestations/sbom"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/frontend/gateway/client"
	gwpb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/frontend/subrequests/outline"
	"github.com/moby/buildkit/frontend/subrequests/targets"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/solver/result"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	// Don't forget to update frontend documentation if you add
	// a new build-arg: frontend/dockerfile/docs/reference.md
	keySyntaxArg = "build-arg:BUILDKIT_SYNTAX"
)

func Build(ctx context.Context, c client.Client) (_ *client.Result, err error) {
	bc, err := dockerui.NewClient(c)
	if err != nil {
		return nil, err
	}
	opts := bc.BuildOpts().Opts
	allowForward, capsError := validateCaps(opts["frontend.caps"])
	if !allowForward && capsError != nil {
		return nil, capsError
	}

	src, err := bc.ReadEntrypoint(ctx, "Dockerfile")
	if err != nil {
		return nil, err
	}

	if _, ok := opts["cmdline"]; !ok {
		if cmdline, ok := opts[keySyntaxArg]; ok {
			p := strings.SplitN(strings.TrimSpace(cmdline), " ", 2)
			res, err := forwardGateway(ctx, c, p[0], cmdline)
			if err != nil && len(errdefs.Sources(err)) == 0 {
				return nil, errors.Wrapf(err, "failed with %s = %s", keySyntaxArg, cmdline)
			}
			return res, err
		} else if ref, cmdline, loc, ok := parser.DetectSyntax(src.Data); ok {
			res, err := forwardGateway(ctx, c, ref, cmdline)
			if err != nil && len(errdefs.Sources(err)) == 0 {
				return nil, wrapSource(err, src.SourceMap, loc)
			}
			return res, err
		}
	}

	if capsError != nil {
		return nil, capsError
	}

	convertOpt := dockerfile2llb.ConvertOpt{
		Config:       bc.Config,
		Client:       bc,
		SourceMap:    src.SourceMap,
		MetaResolver: c,
		Warn: func(msg, url string, detail [][]byte, location *parser.Range) {
			src.Warn(ctx, msg, warnOpts(location, detail, url))
		},
	}

<<<<<<< HEAD
	if res, ok, err := bc.HandleSubrequest(ctx, dockerui.RequestHandler{
		Outline: func(ctx context.Context) (*outline.Outline, error) {
			return dockerfile2llb.Dockefile2Outline(ctx, src.Data, convertOpt)
		},
		ListTargets: func(ctx context.Context) (*targets.List, error) {
			return dockerfile2llb.ListTargets(ctx, src.Data)
		},
	}); err != nil {
=======
	exportMap := len(targetPlatforms) > 1

	if v := opts[keyMultiPlatformArg]; v != "" {
		opts[keyMultiPlatform] = v
	}
	if v := opts[keyMultiPlatform]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, errors.Errorf("invalid boolean value %s", v)
		}
		if !b && exportMap {
			return nil, errors.Errorf("returning multiple target plaforms is not allowed")
		}
		exportMap = b
	}

	expPlatforms := &exptypes.Platforms{
		Platforms: make([]exptypes.Platform, len(targetPlatforms)),
	}
	res := client.NewResult()

	if v, ok := opts[keyHostnameArg]; ok && len(v) > 0 {
		opts[keyHostname] = v
	}

	eg, ctx = errgroup.WithContext(ctx)

	for i, tp := range targetPlatforms {
		func(i int, tp *ocispecs.Platform) {
			eg.Go(func() (err error) {
				defer func() {
					var el *parser.ErrorLocation
					if errors.As(err, &el) {
						err = wrapSource(err, sourceMap, el.Location)
					}
				}()

				st, img, bi, err := dockerfile2llb.Dockerfile2LLB(ctx, dtDockerfile, dockerfile2llb.ConvertOpt{
					Target:            opts[keyTarget],
					MetaResolver:      c,
					BuildArgs:         filter(opts, buildArgPrefix),
					Labels:            filter(opts, labelPrefix),
					CacheIDNamespace:  opts[keyCacheNSArg],
					SessionID:         c.BuildOpts().SessionID,
					BuildContext:      buildContext,
					Excludes:          excludes,
					IgnoreCache:       ignoreCache,
					TargetPlatform:    tp,
					BuildPlatforms:    buildPlatforms,
					ImageResolveMode:  resolveMode,
					PrefixPlatform:    exportMap,
					ExtraHosts:        extraHosts,
					ShmSize:           shmSize,
					Ulimit:            ulimit,
					CgroupParent:      opts[keyCgroupParent],
					ForceNetMode:      defaultNetMode,
					OverrideCopyImage: opts[keyOverrideCopyImage],
					LLBCaps:           &caps,
					SourceMap:         sourceMap,
					Hostname:          opts[keyHostname],
					Warn: func(msg, url string, detail [][]byte, location *parser.Range) {
						if i != 0 {
							return
						}
						c.Warn(ctx, defVtx, msg, warnOpts(sourceMap, location, detail, url))
					},
					ContextByName: contextByNameFunc(c),
				})

				if err != nil {
					return err
				}

				def, err := st.Marshal(ctx)
				if err != nil {
					return errors.Wrapf(err, "failed to marshal LLB definition")
				}

				config, err := json.Marshal(img)
				if err != nil {
					return errors.Wrapf(err, "failed to marshal image config")
				}

				var cacheImports []client.CacheOptionsEntry
				// new API
				if cacheImportsStr := opts[keyCacheImports]; cacheImportsStr != "" {
					var cacheImportsUM []controlapi.CacheOptionsEntry
					if err := json.Unmarshal([]byte(cacheImportsStr), &cacheImportsUM); err != nil {
						return errors.Wrapf(err, "failed to unmarshal %s (%q)", keyCacheImports, cacheImportsStr)
					}
					for _, um := range cacheImportsUM {
						cacheImports = append(cacheImports, client.CacheOptionsEntry{Type: um.Type, Attrs: um.Attrs})
					}
				}
				// old API
				if cacheFromStr := opts[keyCacheFrom]; cacheFromStr != "" {
					cacheFrom := strings.Split(cacheFromStr, ",")
					for _, s := range cacheFrom {
						im := client.CacheOptionsEntry{
							Type: "registry",
							Attrs: map[string]string{
								"ref": s,
							},
						}
						// FIXME(AkihiroSuda): skip append if already exists
						cacheImports = append(cacheImports, im)
					}
				}

				r, err := c.Solve(ctx, client.SolveRequest{
					Definition:   def.ToPB(),
					CacheImports: cacheImports,
				})
				if err != nil {
					return err
				}

				ref, err := r.SingleRef()
				if err != nil {
					return err
				}

				buildinfo, err := json.Marshal(bi)
				if err != nil {
					return errors.Wrapf(err, "failed to marshal build info")
				}

				if !exportMap {
					res.AddMeta(exptypes.ExporterImageConfigKey, config)
					res.AddMeta(exptypes.ExporterBuildInfo, buildinfo)
					res.SetRef(ref)
				} else {
					p := platforms.DefaultSpec()
					if tp != nil {
						p = *tp
					}

					k := platforms.Format(p)
					res.AddMeta(fmt.Sprintf("%s/%s", exptypes.ExporterImageConfigKey, k), config)
					res.AddMeta(fmt.Sprintf("%s/%s", exptypes.ExporterBuildInfo, k), buildinfo)
					res.AddRef(k, ref)
					expPlatforms.Platforms[i] = exptypes.Platform{
						ID:       k,
						Platform: p,
					}
				}
				return nil
			})
		}(i, tp)
	}

	if err := eg.Wait(); err != nil {
>>>>>>> origin/v0.10
		return nil, err
	} else if ok {
		return res, nil
	}

	defer func() {
		var el *parser.ErrorLocation
		if errors.As(err, &el) {
			err = wrapSource(err, src.SourceMap, el.Location)
		}
	}()

	var scanner sbom.Scanner
	if bc.SBOM != nil {
		scanner, err = sbom.CreateSBOMScanner(ctx, c, bc.SBOM.Generator, llb.ResolveImageConfigOpt{
			ResolveMode: opts["image-resolve-mode"],
		})
		if err != nil {
			return nil, err
		}
	}

	scanTargets := sync.Map{}

	rb, err := bc.Build(ctx, func(ctx context.Context, platform *ocispecs.Platform, idx int) (client.Reference, *image.Image, error) {
		opt := convertOpt
		opt.TargetPlatform = platform
		if idx != 0 {
			opt.Warn = nil
		}

		st, img, scanTarget, err := dockerfile2llb.Dockerfile2LLB(ctx, src.Data, opt)
		if err != nil {
			return nil, nil, err
		}

		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "failed to marshal LLB definition")
		}

		r, err := c.Solve(ctx, client.SolveRequest{
			Definition:   def.ToPB(),
			CacheImports: bc.CacheImports,
		})
		if err != nil {
			return nil, nil, err
		}

		ref, err := r.SingleRef()
		if err != nil {
			return nil, nil, err
		}

		p := platforms.DefaultSpec()
		if platform != nil {
			p = *platform
		}
		scanTargets.Store(platforms.Format(platforms.Normalize(p)), scanTarget)

		return ref, img, nil
	})
	if err != nil {
		return nil, err
	}

	if scanner != nil {
		if err := rb.EachPlatform(ctx, func(ctx context.Context, id string, p ocispecs.Platform) error {
			v, ok := scanTargets.Load(id)
			if !ok {
				return errors.Errorf("no scan targets for %s", id)
			}
			target, ok := v.(*dockerfile2llb.SBOMTargets)
			if !ok {
				return errors.Errorf("invalid scan targets for %T", v)
			}

			var opts []llb.ConstraintsOpt
			if target.IgnoreCache {
				opts = append(opts, llb.IgnoreCache)
			}
			att, err := scanner(ctx, id, target.Core, target.Extras, opts...)
			if err != nil {
				return err
			}

			attSolve, err := result.ConvertAttestation(&att, func(st *llb.State) (client.Reference, error) {
				def, err := st.Marshal(ctx)
				if err != nil {
					return nil, err
				}
				r, err := c.Solve(ctx, frontend.SolveRequest{
					Definition: def.ToPB(),
				})
				if err != nil {
					return nil, err
				}
				return r.Ref, nil
			})
			if err != nil {
				return err
			}
			rb.AddAttestation(id, *attSolve)
			return nil
		}); err != nil {
			return nil, err
		}
	}

	return rb.Finalize()
}

func forwardGateway(ctx context.Context, c client.Client, ref string, cmdline string) (*client.Result, error) {
	opts := c.BuildOpts().Opts
	if opts == nil {
		opts = map[string]string{}
	}
	opts["cmdline"] = cmdline
	opts["source"] = ref

	gwcaps := c.BuildOpts().Caps
	var frontendInputs map[string]*pb.Definition
	if (&gwcaps).Supports(gwpb.CapFrontendInputs) == nil {
		inputs, err := c.Inputs(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get frontend inputs")
		}

		frontendInputs = make(map[string]*pb.Definition)
		for name, state := range inputs {
			def, err := state.Marshal(ctx)
			if err != nil {
				return nil, err
			}
			frontendInputs[name] = def.ToPB()
		}
	}

	return c.Solve(ctx, client.SolveRequest{
		Frontend:       "gateway.v0",
		FrontendOpt:    opts,
		FrontendInputs: frontendInputs,
	})
}

func warnOpts(r *parser.Range, detail [][]byte, url string) client.WarnOpts {
	opts := client.WarnOpts{Level: 1, Detail: detail, URL: url}
	if r == nil {
		return opts
	}
	opts.Range = []*pb.Range{{
		Start: pb.Position{
			Line:      int32(r.Start.Line),
			Character: int32(r.Start.Character),
		},
		End: pb.Position{
			Line:      int32(r.End.Line),
			Character: int32(r.End.Character),
		},
	}}
	return opts
}

<<<<<<< HEAD
=======
func contextByNameFunc(c client.Client) func(context.Context, string, string, *ocispecs.Platform) (*llb.State, *dockerfile2llb.Image, *binfotypes.BuildInfo, error) {
	return func(ctx context.Context, name, resolveMode string, p *ocispecs.Platform) (*llb.State, *dockerfile2llb.Image, *binfotypes.BuildInfo, error) {
		named, err := reference.ParseNormalizedNamed(name)
		if err != nil {
			return nil, nil, nil, errors.Wrapf(err, "invalid context name %s", name)
		}
		name = strings.TrimSuffix(reference.FamiliarString(named), ":latest")

		if p == nil {
			pp := platforms.Normalize(platforms.DefaultSpec())
			p = &pp
		}
		if p != nil {
			name := name + "::" + platforms.Format(platforms.Normalize(*p))
			st, img, bi, err := contextByName(ctx, c, name, p, resolveMode)
			if err != nil {
				return nil, nil, nil, err
			}
			if st != nil {
				return st, img, bi, nil
			}
		}
		return contextByName(ctx, c, name, p, resolveMode)
	}
}

func contextByName(ctx context.Context, c client.Client, name string, platform *ocispecs.Platform, resolveMode string) (*llb.State, *dockerfile2llb.Image, *binfotypes.BuildInfo, error) {
	opts := c.BuildOpts().Opts
	v, ok := opts["context:"+name]
	if !ok {
		return nil, nil, nil, nil
	}

	vv := strings.SplitN(v, ":", 2)
	if len(vv) != 2 {
		return nil, nil, nil, errors.Errorf("invalid context specifier %s for %s", v, name)
	}
	switch vv[0] {
	case "docker-image":
		ref := strings.TrimPrefix(vv[1], "//")
		imgOpt := []llb.ImageOption{
			llb.WithCustomName("[context " + name + "] " + ref),
		}
		if platform != nil {
			imgOpt = append(imgOpt, llb.Platform(*platform))
		}

		named, err := reference.ParseNormalizedNamed(ref)
		if err != nil {
			return nil, nil, nil, err
		}

		named = reference.TagNameOnly(named)

		_, data, err := c.ResolveImageConfig(ctx, named.String(), llb.ResolveImageConfigOpt{
			Platform:    platform,
			ResolveMode: resolveMode,
			LogName:     fmt.Sprintf("[context %s] load metadata for %s", name, ref),
		})
		if err != nil {
			return nil, nil, nil, err
		}

		var img dockerfile2llb.Image
		if err := json.Unmarshal(data, &img); err != nil {
			return nil, nil, nil, err
		}
		img.Created = nil

		st := llb.Image(ref, imgOpt...)
		st, err = st.WithImageConfig(data)
		if err != nil {
			return nil, nil, nil, err
		}
		return &st, &img, nil, nil
	case "git":
		st, ok := detectGitContext(v, "1")
		if !ok {
			return nil, nil, nil, errors.Errorf("invalid git context %s", v)
		}
		return st, nil, nil, nil
	case "http", "https":
		st, ok := detectGitContext(v, "1")
		if !ok {
			httpst := llb.HTTP(v, llb.WithCustomName("[context "+name+"] "+v))
			st = &httpst
		}
		return st, nil, nil, nil
	case "local":
		st := llb.Local(vv[1],
			llb.SessionID(c.BuildOpts().SessionID),
			llb.FollowPaths([]string{dockerignoreFilename}),
			llb.SharedKeyHint("context:"+name+"-"+dockerignoreFilename),
			llb.WithCustomName("[context "+name+"] load "+dockerignoreFilename),
			llb.Differ(llb.DiffNone, false),
		)
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		res, err := c.Solve(ctx, client.SolveRequest{
			Evaluate:   true,
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, nil, nil, err
		}
		ref, err := res.SingleRef()
		if err != nil {
			return nil, nil, nil, err
		}
		dt, _ := ref.ReadFile(ctx, client.ReadRequest{
			Filename: dockerignoreFilename,
		}) // error ignored
		var excludes []string
		if len(dt) != 0 {
			excludes, err = dockerignore.ReadAll(bytes.NewBuffer(dt))
			if err != nil {
				return nil, nil, nil, err
			}
		}
		st = llb.Local(vv[1],
			llb.WithCustomName("[context "+name+"] load from client"),
			llb.SessionID(c.BuildOpts().SessionID),
			llb.SharedKeyHint("context:"+name),
			llb.ExcludePatterns(excludes),
		)
		return &st, nil, nil, nil
	case "input":
		inputs, err := c.Inputs(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		st, ok := inputs[vv[1]]
		if !ok {
			return nil, nil, nil, errors.Errorf("invalid input %s for %s", vv[1], name)
		}
		md, ok := opts["input-metadata:"+vv[1]]
		if ok {
			m := make(map[string][]byte)
			if err := json.Unmarshal([]byte(md), &m); err != nil {
				return nil, nil, nil, errors.Wrapf(err, "failed to parse input metadata %s", md)
			}
			var bi *binfotypes.BuildInfo
			if dtbi, ok := m[exptypes.ExporterBuildInfo]; ok {
				var depbi binfotypes.BuildInfo
				if err := json.Unmarshal(dtbi, &depbi); err != nil {
					return nil, nil, nil, errors.Wrapf(err, "failed to parse buildinfo for %s", name)
				}
				bi = &binfotypes.BuildInfo{
					Deps: map[string]binfotypes.BuildInfo{
						strings.SplitN(vv[1], "::", 2)[0]: depbi,
					},
				}
			}
			var img *dockerfile2llb.Image
			if dtic, ok := m[exptypes.ExporterImageConfigKey]; ok {
				st, err = st.WithImageConfig(dtic)
				if err != nil {
					return nil, nil, nil, err
				}
				if err := json.Unmarshal(dtic, &img); err != nil {
					return nil, nil, nil, errors.Wrapf(err, "failed to parse image config for %s", name)
				}
			}
			return &st, img, bi, nil
		}
		return &st, nil, nil, nil
	default:
		return nil, nil, nil, errors.Errorf("unsupported context source %s for %s", vv[0], name)
	}
}

>>>>>>> origin/v0.10
func wrapSource(err error, sm *llb.SourceMap, ranges []parser.Range) error {
	if sm == nil {
		return err
	}
	s := errdefs.Source{
		Info: &pb.SourceInfo{
			Data:       sm.Data,
			Filename:   sm.Filename,
			Language:   sm.Language,
			Definition: sm.Definition.ToPB(),
		},
		Ranges: make([]*pb.Range, 0, len(ranges)),
	}
	for _, r := range ranges {
		s.Ranges = append(s.Ranges, &pb.Range{
			Start: pb.Position{
				Line:      int32(r.Start.Line),
				Character: int32(r.Start.Character),
			},
			End: pb.Position{
				Line:      int32(r.End.Line),
				Character: int32(r.End.Character),
			},
		})
	}
	return errdefs.WithSource(err, s)
}
