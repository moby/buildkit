package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pelletier/go-toml"
)

// Load loads buildkitd config
func Load(r io.Reader) (Config, error) {
	var c Config
	t, err := toml.LoadReader(r)
	if err != nil {
		return c, fmt.Errorf("failed to parse config"+": %w", err)
	}
	err = t.Unmarshal(&c)
	if err != nil {
		return c, fmt.Errorf("failed to parse config"+": %w", err)
	}
	return c, nil
}

// LoadFile loads buildkitd config file
func LoadFile(fp string) (Config, error) {
	f, err := os.Open(fp)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("failed to load config from %s: %w", fp, err)
	}
	defer f.Close()
	return Load(f)
}
