package build

import (
	"encoding/csv"
	"strings"

	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func parseExportCacheCSV(s string) (client.CacheOptionsEntry, error) {
	ex := client.CacheOptionsEntry{
		Type:  "",
		Attrs: map[string]string{},
	}
	csvReader := csv.NewReader(strings.NewReader(s))
	fields, err := csvReader.Read()
	if err != nil {
		return ex, err
	}
	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			return ex, errors.Errorf("invalid value %s", field)
		}
		key := strings.ToLower(parts[0])
		value := parts[1]
		switch key {
		case "type":
			ex.Type = value
		default:
			ex.Attrs[key] = value
		}
	}
	if ex.Type == "" {
		return ex, errors.New("--export-cache requires type=<type>")
	}
	if _, ok := ex.Attrs["mode"]; !ok {
		ex.Attrs["mode"] = "min"
	}
	return ex, nil
}

// ParseExportCache parses --export-cache (and legacy --export-cache-opt)
func ParseExportCache(exportCaches, legacyExportCacheOpts []string) ([]client.CacheOptionsEntry, error) {
	var exports []client.CacheOptionsEntry
	if len(legacyExportCacheOpts) > 0 {
		if len(exportCaches) != 1 {
			return nil, errors.New("--export-cache-opt requires exactly single --export-cache")
		}
	}
	for _, exportCache := range exportCaches {
		legacy := !strings.Contains(exportCache, "type=")
		if legacy {
			logrus.Warnf("--export-cache <ref> --export-cache-opt <opt>=<optval> is deprecated. Please use --export-cache type=registry,ref=<ref>,<opt>=<optval>[,<opt>=<optval>] instead")
			attrs, err := attrMap(legacyExportCacheOpts)
			if err != nil {
				return nil, err
			}
			if _, ok := attrs["mode"]; !ok {
				attrs["mode"] = "min"
			}
			attrs["ref"] = exportCache
			exports = append(exports, client.CacheOptionsEntry{
				Type:  "registry",
				Attrs: attrs,
			})
		} else {
			if len(legacyExportCacheOpts) > 0 {
				return nil, errors.New("--export-cache-opt is not supported for the specified --export-cache. Please use --export-cache type=<type>,<opt>=<optval>[,<opt>=<optval>] instead")
			}
			ex, err := parseExportCacheCSV(exportCache)
			if err != nil {
				return nil, err
			}
			exports = append(exports, ex)
		}
	}
	return exports, nil

}
