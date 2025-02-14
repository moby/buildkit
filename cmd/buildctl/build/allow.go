package build

import (
	"github.com/moby/buildkit/util/entitlements"
)

// ValidateAllow parses --allow
func ValidateAllow(inp []string) error {
	for _, v := range inp {
		_, _, err := entitlements.Parse(v)
		if err != nil {
			return err
		}
	}
	return nil
}
