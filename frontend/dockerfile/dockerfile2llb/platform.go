package dockerfile2llb

import (
	"github.com/containerd/containerd/platforms"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

type platformOpt struct {
	targetPlatform specs.Platform
	buildPlatforms []specs.Platform
	implicitTarget bool
}

func buildPlatformOpt(opt *ConvertOpt) *platformOpt {
	buildPlatforms := opt.BuildPlatforms
	targetPlatform := opt.TargetPlatform
	implicitTargetPlatform := false

	if opt.TargetPlatform != nil && opt.BuildPlatforms == nil {
		buildPlatforms = []specs.Platform{*opt.TargetPlatform}
	}
	if len(buildPlatforms) == 0 {
		buildPlatforms = []specs.Platform{platforms.DefaultSpec()}
	}

	if opt.TargetPlatform == nil {
		implicitTargetPlatform = true
		targetPlatform = &buildPlatforms[0]
	}

	return &platformOpt{
		targetPlatform: *targetPlatform,
		buildPlatforms: buildPlatforms,
		implicitTarget: implicitTargetPlatform,
	}
}
