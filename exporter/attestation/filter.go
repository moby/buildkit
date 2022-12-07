package attestation

import (
	"bytes"

	"github.com/moby/buildkit/solver/result"
)

func Filter(attestations []result.Attestation, include map[string][]byte, exclude map[string][]byte) []result.Attestation {
	if len(include) == 0 && len(exclude) == 0 {
		return attestations
	}

	result := []result.Attestation{}
	for _, att := range attestations {
		meta := att.Metadata
		if meta == nil {
			meta = map[string][]byte{}
		}

		match := true
		for k, v := range include {
			if !bytes.Equal(meta[k], v) {
				match = false
				break
			}
		}
		if !match {
			continue
		}

		for k, v := range exclude {
			if bytes.Equal(meta[k], v) {
				match = false
				break
			}
		}
		if !match {
			continue
		}

		result = append(result, att)
	}
	return result
}
