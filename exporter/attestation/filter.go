package attestation

import (
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/solver/result"
)

func FilterReasons(attestations []exporter.Attestation, reasons []string) (matching []exporter.Attestation, nonMatching []exporter.Attestation) {
	if reasons == nil {
		// don't filter if no filter provided
		return attestations, nil
	}

	for _, att := range attestations {
		target, ok := att.Metadata[result.AttestationReasonKey]
		if ok {
			matched := false
			for _, reason := range reasons {
				if string(target) == reason {
					matched = true
					break
				}
			}
			if matched {
				matching = append(matching, att)
				continue
			}
		}
		nonMatching = append(nonMatching, att)
	}
	return matching, nonMatching
}
