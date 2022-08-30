package attestations

import (
	"encoding/csv"
	"strings"

	"github.com/pkg/errors"
)

const (
	KeyTypeSbom       = "sbom"
	KeyTypeProvenance = "provenance"
)

const (
	defaultSBOMGenerator = "jedevc/buildkit-syft-scanner:latest"
)

func Filter(v map[string]string) map[string]string {
	attests := make(map[string]string)
	for k, v := range v {
		if strings.HasPrefix(k, "attest:") {
			attests[k] = v
			continue
		}
		if strings.HasPrefix(k, "build-arg:BUILDKIT_ATTEST_") {
			attests[k] = v
			continue
		}
	}
	return attests
}

func Parse(v map[string]string) (map[string]map[string]string, error) {
	attests := make(map[string]string)
	for k, v := range v {
		if strings.HasPrefix(k, "attest:") {
			attests[strings.ToLower(strings.TrimPrefix(k, "attest:"))] = v
			continue
		}
		if strings.HasPrefix(k, "build-arg:BUILDKIT_ATTEST_") {
			attests[strings.ToLower(strings.TrimPrefix(k, "build-arg:BUILDKIT_ATTEST_"))] = v
			continue
		}
	}

	out := make(map[string]map[string]string)
	for k, v := range attests {
		attrs := make(map[string]string)
		out[k] = attrs
		if k == KeyTypeSbom {
			attrs["generator"] = defaultSBOMGenerator
		}
		if v == "" {
			continue
		}
		csvReader := csv.NewReader(strings.NewReader(v))
		fields, err := csvReader.Read()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse %s", k)
		}
		for _, field := range fields {
			parts := strings.SplitN(field, "=", 2)
			if len(parts) != 2 {
				parts = append(parts, "")
			}
			attrs[parts[0]] = parts[1]
		}
	}
	return out, nil
}
