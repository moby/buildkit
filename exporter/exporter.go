package exporter

import (
	"context"
	"strings"
	"text/template"

	"github.com/moby/buildkit/cache"
	"github.com/pkg/errors"
)

type Exporter interface {
	Resolve(context.Context, map[string]string) (ExporterInstance, error)
}

type ExporterInstance interface {
	Name() string
	Export(context.Context, Source) (map[string]string, error)
}

type Source struct {
	Ref      cache.ImmutableRef
	Refs     map[string]cache.ImmutableRef
	Metadata map[string][]byte
}

func ResolveNameTemplate(tmpl string, m map[string][]byte) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("name").Parse(tmpl)
	if err != nil {
		return "", errors.Wrapf(err, "parsing %q", tmpl)
	}
	var b strings.Builder
	if err := t.Execute(&b, m); err != nil {
		return "", errors.Wrapf(err, "executing %q", tmpl)
	}
	return b.String(), nil
}
