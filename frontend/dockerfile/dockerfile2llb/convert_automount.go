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
		mount, err := instructions.ParseMount(automountSpec, nil)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse automount %q", automountSpec)
		}

		// Automounts don't support "from" - they always use the build context
		if mount.From != "" {
			return nil, errors.Errorf("automount does not support 'from' option: %q", automountSpec)
		}

		// For cache mounts without a from, use scratch like regular mounts do
		st := opt.buildContext
		if mount.Type == instructions.MountTypeCache {
			st = llb.Scratch()
		}

		runOpt, err := dispatchMount(d, mount, st, opt)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to dispatch automount %q", automountSpec)
		}
		if runOpt != nil {
			out = append(out, runOpt)
		}

		// Track context paths for bind mounts
		if mount.Type == instructions.MountTypeBind {
			d.ctxPaths[path.Join("/", filepath.ToSlash(mount.Source))] = struct{}{}
		}
	}

	return out, nil
}
