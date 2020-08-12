package authprovider

import (
	"crypto/rand"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
	"github.com/pkg/errors"
)

type tokenSeeds struct {
	mu  sync.Mutex
	dir string
}

type seed struct {
	Seed []byte
}

func (ts *tokenSeeds) getSeed(host string) ([]byte, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if err := os.MkdirAll(ts.dir, 0755); err != nil {
		return nil, err
	}

	l := flock.New(filepath.Join(ts.dir, ".token_seed.lock"))
	if err := l.Lock(); err != nil {
		return nil, err
	}
	defer l.Unlock()

	// we include client side randomness to avoid chosen plaintext attack from the daemon side
	fp := filepath.Join(ts.dir, ".token_seed")
	dt, err := ioutil.ReadFile(fp)
	m := map[string]seed{}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(dt, &m); err != nil {
			return nil, errors.Wrapf(err, "failed to parse %s", fp)
		}
	}
	v, ok := m[host]
	if !ok {
		v = seed{Seed: newSeed()}
	}

	m[host] = v

	dt, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}

	if err := ioutil.WriteFile(fp, dt, 0600); err != nil {
		return nil, err
	}
	return v.Seed, nil
}

func newSeed() []byte {
	b := make([]byte, 16)
	rand.Read(b)
	return b
}
