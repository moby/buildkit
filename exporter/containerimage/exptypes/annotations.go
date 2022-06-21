package exptypes

import (
	"fmt"
	"regexp"

	"github.com/containerd/containerd/platforms"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	AnnotationIndex              = "index"
	AnnotationIndexDescriptor    = "index-descriptor"
	AnnotationManifest           = "manifest"
	AnnotationManifestDescriptor = "manifest-descriptor"
)

var (
	keyAnnotationRegexp       = regexp.MustCompile(`^annotation(?:-([a-z-]+))?(?:\[([A-Za-z0-9_/-]+)\])?\.(\S+)$`)
	keyAnnotationPrefixRegexp = regexp.MustCompile(`^annotation[-.\[]`)
)

type AnnotationKey struct {
	Type     string
	Platform *ocispecs.Platform
	Key      string
}

func (k AnnotationKey) String() string {
	prefix := "annotation"

	switch k.Type {
	case "":
	case AnnotationManifest, AnnotationManifestDescriptor:
		prefix += fmt.Sprintf("-%s", k.Type)
		if p := k.PlatformString(); p != "" {
			prefix += fmt.Sprintf("[%s]", p)
		}
	case AnnotationIndex, AnnotationIndexDescriptor:
		prefix += "-" + k.Type
	default:
		panic("unknown annotation type")
	}

	return fmt.Sprintf("%s.%s", prefix, k.Key)
}

func (k AnnotationKey) PlatformString() string {
	if k.Platform == nil {
		return ""
	}
	return platforms.Format(*k.Platform)
}

func AnnotationIndexKey(key string) string {
	return AnnotationKey{
		Type: AnnotationIndex,
		Key:  key,
	}.String()
}

func AnnotationIndexDescriptorKey(key string) string {
	return AnnotationKey{
		Type: AnnotationIndexDescriptor,
		Key:  key,
	}.String()
}

func AnnotationManifestKey(p *ocispecs.Platform, key string) string {
	return AnnotationKey{
		Type:     AnnotationManifest,
		Platform: p,
		Key:      key,
	}.String()
}

func AnnotationManifestDescriptorKey(p *ocispecs.Platform, key string) string {
	return AnnotationKey{
		Type:     AnnotationManifestDescriptor,
		Platform: p,
		Key:      key,
	}.String()
}

func ParseAnnotationKey(result string) (*AnnotationKey, error) {
	if !keyAnnotationPrefixRegexp.MatchString(result) {
		return nil, nil
	}

	groups := keyAnnotationRegexp.FindStringSubmatch(result)
	if groups == nil {
		return nil, errors.Errorf("could not fully parse annotation %s", result)
	}

	tp, platform, key := groups[1], groups[2], groups[3]
	switch tp {
	case AnnotationIndex, AnnotationIndexDescriptor, AnnotationManifest, AnnotationManifestDescriptor:
	case "":
		tp = AnnotationManifest
	default:
		return nil, errors.Errorf("unrecognized annotation type %s", tp)
	}

	var ociPlatform *ocispecs.Platform
	if platform != "" {
		p, err := platforms.Parse(platform)
		if err != nil {
			return nil, err
		}
		ociPlatform = &p
	}

	annotation := &AnnotationKey{
		Type:     tp,
		Platform: ociPlatform,
		Key:      key,
	}
	return annotation, nil
}
