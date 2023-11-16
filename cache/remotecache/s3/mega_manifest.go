package s3

import (
	_ "embed"
	"encoding/json"
	"strings"

	v1 "github.com/moby/buildkit/cache/remotecache/v1"
)

//go:embed mega_manifest.json
var megaManifest string

func loadMegaManifest(config *v1.CacheConfig) error {
	return json.NewDecoder(strings.NewReader(megaManifest)).Decode(config)
}
