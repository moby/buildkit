package dockerfile2llb

import (
	"path"
	"path/filepath"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/pkg/errors"
)

// dispatchAutomounts converts automount specifications to LLB run options.
// Automounts are applied to all RUN commands, allowing users to inject mounts
// like CA certificates or proxy configuration without modifying the Dockerfile.
func dispatchAutomounts(d *dispatchState, opt dispatchOpt) ([]llb.RunOption, error) {
	if len(opt.automounts) == 0 {
		return nil, nil
	}

	var out []llb.RunOption

	for _, automountSpec := range opt.automounts {
		// Use a simple pass-through expander for automount specs.
		// NOTE: This intentionally does NOT perform any variable or build-arg expansion.
		// Automounts are specified at the CLI level and treated as literal values, so
		// patterns like "source=${MY_VAR}" will not be expanded here.
		expander := func(s string) (string, error) {
			return s, nil
		}
		mount, err := instructions.ParseMount(automountSpec, expander)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse automount %q", automountSpec)
		}

		// Determine the source state for the mount
		st := opt.buildContext
		
		if mount.From != "" {
			// Check if 'from' references a Dockerfile stage (disallowed for automounts)
			if _, ok := opt.allDispatchStates.findStateByName(mount.From); ok {
				return nil, errors.Errorf("automount cannot reference Dockerfile stage %q: only external images and named contexts are allowed", mount.From)
			}
			
			// Use llb.Image() for external images/named contexts
			// This is simpler than creating an unregistered dispatch state since we don't
			// need the full dispatch machinery for external references in automounts
			st = llb.Image(mount.From)
		} else if mount.Type == instructions.MountTypeCache {
			// For cache mounts without a from, use scratch like regular mounts do
			st = llb.Scratch()
		}

		runOpt, err := dispatchMount(d, mount, st, opt, nil)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to dispatch automount %q", automountSpec)
		}
		if runOpt != nil {
			out = append(out, runOpt)
		}

		// Track context paths for bind mounts
		if mount.Type == instructions.MountTypeBind && mount.From == "" {
			d.ctxPaths[path.Join("/", filepath.ToSlash(mount.Source))] = struct{}{}
		}
	}

	return out, nil
}
