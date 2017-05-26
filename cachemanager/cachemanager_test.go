package cachemanager

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCacheManager(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "cachemanager")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	cm, err := NewCacheManager(CacheManagerOpt{Root: tmpdir})
	assert.NoError(t, err)

	err = cm.Close()
	assert.NoError(t, err)
}
