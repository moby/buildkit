package contentutil

import (
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/containerd/v2/pkg/reference"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// DistributionSourceAnnotation is the image distribution source annotation prefix.
	DistributionSourceAnnotation = labels.LabelDistributionSource
	// DistributionSourceRefAnnotation stores the source reference from an importer.
	DistributionSourceRefAnnotation = labels.LabelDistributionSource + ".ref"
)

// AddDistributionSource adds the registry source for imageRef to desc.
func AddDistributionSource(desc ocispecs.Descriptor, imageRef string) (ocispecs.Descriptor, string, error) {
	refspec, err := reference.Parse(imageRef)
	if err != nil {
		return desc, "", err
	}

	u, err := url.Parse("dummy://" + refspec.Locator)
	if err != nil {
		return desc, "", err
	}

	source, repo := u.Hostname(), strings.TrimPrefix(u.Path, "/")
	key := DistributionSourceAnnotation + "." + source
	annotations := maps.Clone(desc.Annotations)
	if annotations == nil {
		annotations = make(map[string]string)
	}
	repos := strings.Split(annotations[key], ",")
	if !slices.Contains(repos, repo) {
		repos = append(repos, repo)
	}
	annotations[key] = strings.Join(repos, ",")
	desc.Annotations = annotations

	return desc, key, nil
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
	repoLabel, ok := info.Labels[DistributionSourceAnnotation+"."+source]
	if !ok || repoLabel == "" {
		return false, nil
	}

	if slices.Contains(strings.Split(repoLabel, ","), target) {
		return true, nil
	}
	return false, nil
}
