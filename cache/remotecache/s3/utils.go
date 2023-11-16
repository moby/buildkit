package s3

import (
	"fmt"
	"strings"

	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
)

func outputKey[S ~string](dgst S, output solver.Index) digest.Digest {
	if strings.HasPrefix(string(dgst), "random:") {
		return digest.Digest("random:" + strings.TrimPrefix(digest.FromBytes([]byte(fmt.Sprintf("%s@%d", dgst, output))).String(), digest.Canonical.String()+":"))
	}
	return digest.FromBytes([]byte(fmt.Sprintf("%s@%d", dgst, output)))
}

func isCachableKey[S ~string](inpKey S) bool {
	if strings.HasPrefix(string(inpKey), "random:") {
		return false
	}
	return strings.Contains(string(inpKey), ":")
}
