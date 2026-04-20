package dockerfile2llb

import (
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestCloneCopiesMutableImageConfig(t *testing.T) {
	t.Parallel()

	interval := 2 * time.Second
	src := dockerspec.DockerOCIImage{
		Image: ocispecs.Image{
			History: []ocispecs.History{
				{CreatedBy: "RUN echo base"},
			},
		},
		Config: dockerspec.DockerOCIImageConfig{
			DockerOCIImageConfigExt: dockerspec.DockerOCIImageConfigExt{
				Healthcheck: &dockerspec.HealthcheckConfig{
					Test:     []string{"CMD", "true"},
					Interval: interval,
				},
				OnBuild: []string{"RUN echo base"},
				Shell:   []string{"/bin/sh", "-c"},
			},
		},
	}
	src.Config.ExposedPorts = map[string]struct{}{"80/tcp": {}}
	src.Config.Env = []string{"PATH=/bin"}
	src.Config.Cmd = []string{"true"}
	src.Config.Entrypoint = []string{"/entrypoint"}
	src.Config.Volumes = map[string]struct{}{"/data": {}}
	src.Config.Labels = map[string]string{"marker": "base"}

	cloned := clone(src)

	cloned.Config.ExposedPorts["443/tcp"] = struct{}{}
	cloned.Config.Env[0] = "PATH=/usr/bin"
	cloned.Config.Cmd[0] = "false"
	cloned.Config.Entrypoint[0] = "/bin/other"
	cloned.Config.Volumes["/cache"] = struct{}{}
	cloned.Config.Labels["marker"] = "child"
	cloned.Config.OnBuild[0] = "RUN echo child"
	cloned.Config.Shell[0] = "/bin/bash"
	cloned.Config.Healthcheck.Test[1] = "false"
	cloned.History[0].CreatedBy = "RUN echo child"

	_, ok := src.Config.ExposedPorts["443/tcp"]
	require.False(t, ok)
	require.Equal(t, "PATH=/bin", src.Config.Env[0])
	require.Equal(t, "true", src.Config.Cmd[0])
	require.Equal(t, "/entrypoint", src.Config.Entrypoint[0])
	_, ok = src.Config.Volumes["/cache"]
	require.False(t, ok)
	require.Equal(t, "base", src.Config.Labels["marker"])
	require.Equal(t, "RUN echo base", src.Config.OnBuild[0])
	require.Equal(t, "/bin/sh", src.Config.Shell[0])
	require.Equal(t, "true", src.Config.Healthcheck.Test[1])
	require.Equal(t, "RUN echo base", src.History[0].CreatedBy)
}
