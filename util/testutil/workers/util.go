package workers

import (
	"fmt"

	"github.com/moby/buildkit/util/testutil/integration"
)

func withOTELSocketPath(socketPath string) integration.ConfigUpdater {
	return otelSocketPath(socketPath)
}

type otelSocketPath string

func (osp otelSocketPath) UpdateConfigFile(in string) string {
	return fmt.Sprintf(`%s

[otel]
  socketPath = %q
`, in, osp)
}
