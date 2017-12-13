package builder

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/pkg/errors"
)

const (
	LocalNameContext      = "context"
	LocalNameDockerfile   = "dockerfile"
	keyTarget             = "target"
	keyFilename           = "filename"
	exporterImageConfig   = "containerimage.config"
	defaultDockerfileName = "Dockerfile"
	buildArgPrefix        = "build-arg:"
	gitPrefix             = "git://"
)

func Build(ctx context.Context, c client.Client) error {
	opts := c.Opts()

	filename := opts[keyFilename]
	if filename == "" {
		filename = defaultDockerfileName
	}
	if path.Base(filename) != filename {
		return errors.Errorf("invalid filename: %s", filename)
	}

	src := llb.Local(LocalNameDockerfile,
		llb.IncludePatterns([]string{filename}),
		llb.SessionID(c.SessionID()),
	)
	var buildContext *llb.State
	if strings.HasPrefix(opts[LocalNameContext], gitPrefix) {
		src = parseGitSource(opts[LocalNameContext])
		buildContext = &src
	}
	def, err := src.Marshal()
	if err != nil {
		return err
	}

	ref, err := c.Solve(ctx, def.ToPB(), "", nil, false)
	if err != nil {
		return err
	}

	dtDockerfile, err := ref.ReadFile(ctx, filename)
	if err != nil {
		return err
	}

	st, img, err := dockerfile2llb.Dockerfile2LLB(ctx, dtDockerfile, dockerfile2llb.ConvertOpt{
		Target:       opts[keyTarget],
		MetaResolver: c,
		BuildArgs:    filterBuildArgs(opts),
		SessionID:    c.SessionID(),
		BuildContext: buildContext,
	})

	if err != nil {
		return err
	}

	def, err = st.Marshal()
	if err != nil {
		return err
	}

	config, err := json.Marshal(img)
	if err != nil {
		return err
	}

	_, err = c.Solve(ctx, def.ToPB(), "", map[string][]byte{
		exporterImageConfig: config,
	}, true)
	if err != nil {
		return err
	}
	return nil
}

func filterBuildArgs(opt map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range opt {
		if strings.HasPrefix(k, buildArgPrefix) {
			m[strings.TrimPrefix(k, buildArgPrefix)] = v
		}
	}
	return m
}

func parseGitSource(ref string) llb.State {
	ref = strings.TrimPrefix(ref, gitPrefix)
	parts := strings.SplitN(ref, "#", 2)
	branch := ""
	if len(parts) > 1 {
		branch = parts[1]
	}
	return llb.Git(parts[0], branch)
}
