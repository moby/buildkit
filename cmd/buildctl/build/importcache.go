package build

import (
	"encoding/csv"
	"strings"

	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func parseImportCacheCSV(s string) (client.CacheOptionsEntry, error) {
	im := client.CacheOptionsEntry{
		Type:  "",
		Attrs: map[string]string{},
	}
	csvReader := csv.NewReader(strings.NewReader(s))
	fields, err := csvReader.Read()
	if err != nil {
		return im, err
	}
	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			return im, errors.Errorf("invalid value %s", field)
		}
		key := strings.ToLower(parts[0])
		value := parts[1]
		switch key {
		case "type":
			im.Type = value
		default:
			im.Attrs[key] = value
		}
	}
	if im.Type == "" {
		return im, errors.New("--import-cache requires type=<type>")
	}
	return im, nil
}

// ParseImportCache parses --import-cache
func ParseImportCache(importCaches []string) ([]client.CacheOptionsEntry, error) {
	var imports []client.CacheOptionsEntry
	for _, importCache := range importCaches {
		legacy := !strings.Contains(importCache, "type=")
		if legacy {
			logrus.Warn("--import-cache <ref> is deprecated. Please use --import-cache type=registry,ref=<ref>,<opt>=<optval>[,<opt>=<optval>] instead.")
			imports = append(imports, client.CacheOptionsEntry{
				Type:  "registry",
				Attrs: map[string]string{"ref": importCache},
			})
		} else {
			im, err := parseImportCacheCSV(importCache)
			if err != nil {
				return nil, err
			}
			imports = append(imports, im)
		}
	}
	return imports, nil
}
