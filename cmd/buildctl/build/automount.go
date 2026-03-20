package build

import (
	"fmt"
)

const automountPrefix = "automount:"

// ParseAutomount parses --automount flags and returns frontend attributes
// that can be passed to the dockerfile frontend. Each automount is stored
// as a separate frontend attribute with a numeric index (automount:0, automount:1, etc.)
func ParseAutomount(sl []string) map[string]string {
	if len(sl) == 0 {
		return nil
	}
	attrs := make(map[string]string, len(sl))
	for i, v := range sl {
		key := fmt.Sprintf("%s%d", automountPrefix, i)
		attrs[key] = v
	}
	return attrs
}
