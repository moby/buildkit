package contentutil

import (
	"net/url"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/reference"
)

// AddSourceAnnotation records ref as a distribution source of a blob in
// annotations, under the key that the containerd pusher reads to mount the
// blob from another repository instead of uploading it. It returns that key.
func AddSourceAnnotation(annotations map[string]string, ref string) (string, error) {
	refspec, err := reference.Parse(ref)
	if err != nil {
		return "", err
	}
	u, err := url.Parse("dummy://" + refspec.Locator)
	if err != nil {
		return "", err
	}

	source, repo := u.Hostname(), strings.TrimPrefix(u.Path, "/")
	key := "containerd.io/distribution.source." + source
	var repos []string
	if existing, ok := annotations[key]; ok {
		repos = strings.Split(existing, ",")
	}
	if !slices.Contains(repos, repo) {
		repos = append(repos, repo)
	}
	annotations[key] = strings.Join(repos, ",")
	return key, nil
}

func HasSource(info content.Info, refspec reference.Spec) (bool, error) {
	u, err := url.Parse("dummy://" + refspec.Locator)
	if err != nil {
		return false, err
	}

	if info.Labels == nil {
		return false, nil
	}

	source, target := u.Hostname(), strings.TrimPrefix(u.Path, "/")
	repoLabel, ok := info.Labels["containerd.io/distribution.source."+source]
	if !ok || repoLabel == "" {
		return false, nil
	}

	if slices.Contains(strings.Split(repoLabel, ","), target) {
		return true, nil
	}
	return false, nil
}
