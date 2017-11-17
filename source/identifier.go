package source

import (
	"strings"

	"github.com/containerd/containerd/reference"
	"github.com/pkg/errors"
)

var (
	errInvalid  = errors.New("invalid")
	errNotFound = errors.New("not found")
)

const (
	DockerImageScheme = "docker-image"
	GitScheme         = "git"
	LocalScheme       = "local"
)

type Identifier interface {
	ID() string // until sources are in process this string comparison could be avoided
}

func FromString(s string) (Identifier, error) {
	// TODO: improve this
	parts := strings.SplitN(s, "://", 2)
	if len(parts) != 2 {
		return nil, errors.Wrapf(errInvalid, "failed to parse %s", s)
	}

	switch parts[0] {
	case DockerImageScheme:
		return NewImageIdentifier(parts[1])
	case GitScheme:
		return NewGitIdentifier(parts[1])
	case LocalScheme:
		return NewLocalIdentifier(parts[1])
	default:
		return nil, errors.Wrapf(errNotFound, "unknown schema %s", parts[0])
	}
}

type ImageIdentifier struct {
	Reference reference.Spec
}

func NewImageIdentifier(str string) (*ImageIdentifier, error) {
	ref, err := reference.Parse(str)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if ref.Object == "" {
		return nil, errors.WithStack(reference.ErrObjectRequired)
	}
	return &ImageIdentifier{Reference: ref}, nil
}

func (_ *ImageIdentifier) ID() string {
	return DockerImageScheme
}

type LocalIdentifier struct {
	Name            string
	SessionID       string
	IncludePatterns []string
}

func NewLocalIdentifier(str string) (*LocalIdentifier, error) {
	return &LocalIdentifier{Name: str}, nil
}

func (*LocalIdentifier) ID() string {
	return LocalScheme
}
