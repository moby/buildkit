package main

import (
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/frontend/gateway/grpcclient"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func main() {
	if err := run(); err != nil {
		logrus.Errorf("fatal error: %+v", err)
		panic(err)
	}
}

func run() error {
	c, err := grpcclient.Current()
	if err != nil {
		return errors.Wrap(err, "failed to create client")
	}

	return builder.Build(appcontext.Context(), c)
}
