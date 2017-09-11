package dockerfile2llb

import (
	"testing"

	"github.com/moby/buildkit/util/appcontext"
	"github.com/stretchr/testify/assert"
)

func TestDockerfileParsing(t *testing.T) {
	df := `FROM busybox
ENV FOO bar
COPY f1 f2 /sub/
RUN ls -l
`
	_, _, err := Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	assert.NoError(t, err)

	df = `FROM busybox AS foo
ENV FOO bar
FROM foo
COPY --from=foo f1 /
COPY --from=0 f2 /
	`
	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	assert.NoError(t, err)

	df = `FROM busybox AS foo
ENV FOO bar
FROM foo
COPY --from=foo f1 /
COPY --from=0 f2 /
	`
	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{
		Target: "Foo",
	})
	assert.NoError(t, err)

	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{
		Target: "nosuch",
	})
	assert.Error(t, err)
}
