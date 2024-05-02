package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckSourceIsAllowed(t *testing.T) {
	makeGatewayFrontend := func(sources []string) (*gatewayFrontend, error) {
		gw, err := NewGatewayFrontend(nil, sources)
		if err != nil {
			return nil, err
		}
		gw1 := gw.(*gatewayFrontend)
		return gw1, nil
	}

	var gw *gatewayFrontend
	var err error

	// no restrictions
	gw, err = makeGatewayFrontend([]string{})
	assert.NoError(t, err)
	err = gw.checkSourceIsAllowed("anything")
	assert.NoError(t, err)

	gw, err = makeGatewayFrontend([]string{"docker-registry.wikimedia.org/repos/releng/blubber/buildkit:9.9.9"})
	assert.NoError(t, err)
	err = gw.checkSourceIsAllowed("docker-registry.wikimedia.org/repos/releng/blubber/buildkit")
	assert.NoError(t, err)
	err = gw.checkSourceIsAllowed("docker-registry.wikimedia.org/repos/releng/blubber/buildkit:v1.2.3")
	assert.NoError(t, err)
	err = gw.checkSourceIsAllowed("docker-registry.wikimedia.org/something-else")
	assert.Error(t, err)

	gw, err = makeGatewayFrontend([]string{"alpine"})
	assert.NoError(t, err)
	err = gw.checkSourceIsAllowed("alpine")
	assert.NoError(t, err)
	err = gw.checkSourceIsAllowed("library/alpine")
	assert.NoError(t, err)
	err = gw.checkSourceIsAllowed("docker.io/library/alpine")
	assert.NoError(t, err)
}
