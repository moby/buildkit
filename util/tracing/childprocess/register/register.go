package register

import (
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/tracing/childprocess"
)

func init() {
	appcontext.Register(childprocess.ContextFromEnv)
}
