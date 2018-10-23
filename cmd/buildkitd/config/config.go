package config

import (
	"io"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/pkg/errors"
)

// Config provides containerd configuration data for the server
type Config struct {
	Debug bool `toml:"debug"`

	// Root is the path to a directory where buildkit will store persistent data
	Root string `toml:"root"`

	// GRPC configuration settings
	GRPC GRPCConfig `toml:"grpc"`

	Workers struct {
		OCI        OCIConfig        `toml:"oci"`
		Containerd ContainerdConfig `toml:"containerd"`
	} `toml:"worker"`

	Registries map[string]RegistryConfig `toml:"registry"`
}

type GRPCConfig struct {
	Address      []string `toml:"address"`
	DebugAddress string   `toml:"debugAddress"`
	UID          int      `toml:"uid"`
	GID          int      `toml:"gid"`

	TLS TLSConfig `toml:"tls"`
	// MaxRecvMsgSize int    `toml:"max_recv_message_size"`
	// MaxSendMsgSize int    `toml:"max_send_message_size"`
}

type RegistryConfig struct {
	Mirrors   []string `toml:"mirrors"`
	PlainHTTP bool     `toml:"http"`
	ExtraCA   string   `toml:"ca"`
}

type TLSConfig struct {
	Cert string `toml:"cert"`
	Key  string `toml:"key"`
	CA   string `toml:"ca"`
}

type OCIConfig struct {
	Enabled     *bool             `toml:"enabled"`
	Labels      map[string]string `toml:"labels"`
	Platforms   []string          `toml:"platforms"`
	Snapshotter string            `toml:"snapshotter"`
	Rootless    bool              `toml:"rootless"`
	GCPolicy    []GCPolicy        `toml:"gcpolicy"`
}

type ContainerdConfig struct {
	Address   string            `toml:"address"`
	Enabled   *bool             `toml:"enabled"`
	Labels    map[string]string `toml:"labels"`
	Platforms []string          `toml:"platforms"`
	GCPolicy  []GCPolicy        `toml:"gcpolicy"`
	Namespace string            `toml:"namespace"`
}

type GCPolicy struct {
	All          bool     `toml:"all"`
	KeepBytes    int64    `toml:"keepBytes"`
	KeepDuration int64    `toml:"keepDuration"`
	Filters      []string `toml:"filters"`
}

func Load(r io.Reader) (Config, *toml.MetaData, error) {
	var c Config
	md, err := toml.DecodeReader(r, &c)
	if err != nil {
		return c, nil, errors.Wrap(err, "failed to parse config")
	}
	return c, &md, c.checkFileReferences()
}

func LoadFile(fp string) (Config, *toml.MetaData, error) {
	f, err := os.Open(fp)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil, nil
		}
		return Config{}, nil, errors.Wrapf(err, "failed to load config from %s", fp)
	}
	defer f.Close()
	return Load(f)
}

// checkFileReferences iterates over all files mentioned in the config and checks if they are readable
func (cfg Config) checkFileReferences() error {
	// collect file references from config
	fileReferences := []string{
		cfg.GRPC.TLS.CA,
		cfg.GRPC.TLS.Cert,
		cfg.GRPC.TLS.Key,
	}
	for _, registryConfig := range cfg.Registries {
		fileReferences = append(fileReferences, registryConfig.ExtraCA)
	}
	// check all references for readability
	for _, file := range fileReferences {
		if file != "" {
			if err := checkFile(file); err != nil {
				return errors.Wrapf(err, "file '%v' not valid", file)
			}
		}
	}
	return nil
}

func checkFile(file string) error {
	// check existence
	stat, err := os.Stat(file)
	if err != nil {
		return err
	}
	// check if regular
	if stat.Mode().IsRegular() {
		return errors.New("expect regular file, got something else (mode: " + stat.Mode().String() + ")")
	}
	// check if readable
	f, err := os.OpenFile(file, os.O_RDONLY, 0400)
	if err != nil {
		return errors.Wrap(err, "failed to open file for reading")
	}
	if err = f.Close(); err != nil {
		return errors.Wrap(err, "failed to close file")
	}
	return nil
}
