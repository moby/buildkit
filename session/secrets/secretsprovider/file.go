package secretsprovider

import (
	"context"
	"io/ioutil"
	"os"
	"runtime"
	"strings"

	"github.com/moby/buildkit/session/secrets"
	"github.com/pkg/errors"
)

type FileSource struct {
	ID       string
	FilePath string
	Env      string
}

func NewFileStore(files []FileSource) (secrets.SecretStore, error) {
	m := map[string]FileSource{}
	for _, f := range files {
		if f.ID == "" {
			return nil, errors.Errorf("secret missing ID")
		}
		if f.Env == "" && f.FilePath == "" {
			if hasEnv(f.ID) {
				f.Env = f.ID
			} else {
				f.FilePath = f.ID
			}
		}
		if f.FilePath != "" {
			fi, err := os.Stat(f.FilePath)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to stat %s", f.FilePath)
			}
			if fi.Size() > MaxSecretSize {
				return nil, errors.Errorf("secret %s too big. max size 500KB", f.ID)
			}
		}
		m[f.ID] = f
	}
	return &fileStore{
		m: m,
	}, nil
}

type fileStore struct {
	m map[string]FileSource
}

func (fs *fileStore) GetSecret(ctx context.Context, id string) ([]byte, error) {
	v, ok := fs.m[id]
	if !ok {
		return nil, errors.WithStack(secrets.ErrNotFound)
	}
	if v.Env != "" {
		return []byte(os.Getenv(v.Env)), nil
	}
	dt, err := ioutil.ReadFile(v.FilePath)
	if err != nil {
		return nil, err
	}
	return dt, nil
}

func hasEnv(name string) bool {
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if runtime.GOOS == "windows" {
			// Environment variable are case-insensitive on Windows. PaTh, path and PATH are equivalent.
			if strings.EqualFold(parts[0], name) {
				return true
			}
		}
		if parts[0] == name {
			return true
		}
	}
	return false
}
