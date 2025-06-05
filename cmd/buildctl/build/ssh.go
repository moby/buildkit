package build

import (
	"strconv"
	"strings"

	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/pkg/errors"
)

// ParseSSH parses --ssh
func ParseSSH(inp []string) ([]sshprovider.AgentConfig, error) {
	configs := make([]sshprovider.AgentConfig, 0, len(inp))
	for _, v := range inp {
		key, val, ok := strings.Cut(v, "=")
		cfg := sshprovider.AgentConfig{
			ID: key,
		}
		if ok {
			paths := strings.Split(val, ",")
			cfg.Paths = make([]string, 0, len(paths))

			for _, p := range paths {
				key, val, ok := strings.Cut(p, "=")
				if ok && key == "raw" {
					b, err := strconv.ParseBool(val)
					if err != nil {
						return nil, errors.Wrapf(err, "invalid value for 'raw': %s", val)
					}
					cfg.Raw = b
				} else {
					cfg.Paths = append(cfg.Paths, p)
				}
			}
		}
		if cfg.Raw && len(cfg.Paths) != 1 {
			return nil, errors.Errorf("raw mode must supply exactly one socket path for %q", cfg.ID)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}
