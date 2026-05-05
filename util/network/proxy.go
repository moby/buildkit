package network

import (
	"context"
	"sync"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// ProxyPolicy authorizes requests made through a BuildKit-owned exec proxy.
type ProxyPolicy interface {
	Evaluate(context.Context, *pb.Op) (bool, error)
}

// ProxyNamespace is implemented by network namespaces that expose an internal
// HTTP(S) proxy to the container.
type ProxyNamespace interface {
	ProxyEnv() []string
	ProxyCACert() []byte
}

type ProxyMaterial struct {
	URL    string
	Digest digest.Digest
}

type ProxyIncomplete struct {
	Method   string
	URL      string
	FinalURL string
	Reason   string
}

type ProxyCapture struct {
	mu         sync.Mutex
	materials  []ProxyMaterial
	incomplete []ProxyIncomplete
}

func NewProxyCapture() *ProxyCapture {
	return &ProxyCapture{}
}

func (c *ProxyCapture) AddMaterial(m ProxyMaterial) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.materials = append(c.materials, m)
}

func (c *ProxyCapture) AddIncomplete(in ProxyIncomplete) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.incomplete = append(c.incomplete, in)
}

func (c *ProxyCapture) Materials() []ProxyMaterial {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ProxyMaterial, len(c.materials))
	copy(out, c.materials)
	return out
}

func (c *ProxyCapture) Incomplete() []ProxyIncomplete {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ProxyIncomplete, len(c.incomplete))
	copy(out, c.incomplete)
	return out
}
