package main

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
)

func Load(r io.Reader) (config.Config, error) {
	var c config.Config
	t, err := toml.LoadReader(r)
	if err != nil {
		return c, errors.Wrap(err, "failed to parse config")
	}
	err = t.Unmarshal(&c)
	if err != nil {
		return c, errors.Wrap(err, "failed to parse config")
	}
	return c, nil
}

func LoadFile(fp string) (config.Config, error) {
	f, err := os.Open(fp)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.Config{}, nil
		}
		return config.Config{}, errors.Wrapf(err, "failed to load config from %s", fp)
	}
	defer f.Close()
	return Load(f)
}

// parseBoolOrAuto returns (nil, nil) if s is "auto"
func parseBoolOrAuto(s string) (*bool, error) {
	if s == "" || strings.ToLower(s) == "auto" {
		return nil, nil
	}
	b, err := strconv.ParseBool(s)
	return &b, err
}
