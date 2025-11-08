package build

import (
	"fmt"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
)

// ParseOCILayout parses --oci-layout
func ParseOCILayout(layouts []string) (map[string]content.Store, error) {
	contentStores := make(map[string]content.Store)
	for _, idAndDir := range layouts {
		parts := strings.SplitN(idAndDir, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("oci-layout option must be 'id=path/to/layout', instead had invalid %s", idAndDir)
		}
		cs, err := local.NewStore(parts[1])
		if err != nil {
			return nil, fmt.Errorf("oci-layout context at %s failed to initialize: %w", parts[1], err)
		}
		contentStores[parts[0]] = cs
	}

	return contentStores, nil
}
