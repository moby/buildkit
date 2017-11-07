package main

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (
	keyTarget             = "target"
	keyFilename           = "filename"
	exporterImageConfig   = "containerimage.config"
	defaultDockerfileName = "Dockerfile"
	localNameDockerfile   = "dockerfile"
	buildArgPrefix        = "build-arg:"
	localNameContext      = "context"
	gitPrefix             = "git://"
)

func main() {
	if err := run(); err != nil {
		logrus.Errorf("fatal error: %+v", err)
		panic(err)
	}
}

func run() error {
	c, err := client.Current()
	if err != nil {
		return errors.Wrap(err, "failed to create client")
	}

	ctx := appcontext.Context()

	opts := c.Opts()

	filename := opts[keyFilename]
	if filename == "" {
		filename = defaultDockerfileName
	}
	if path.Base(filename) != filename {
		return errors.Errorf("invalid filename: %s", filename)
	}

	src := llb.Local(localNameDockerfile,
		llb.IncludePatterns([]string{filename}),
		llb.SessionID(c.SessionID()),
	)
	var buildContext *llb.State
	if strings.HasPrefix(opts[localNameContext], gitPrefix) {
		src = parseGitSource(opts[localNameContext])
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
