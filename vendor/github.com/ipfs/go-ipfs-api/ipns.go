package shell

import (
	"context"
	"time"
)

type PublishResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Publish updates a mutable name to point to a given value
func (s *Shell) Publish(node string, value string) error {
	var pubResp PublishResponse
	req := s.Request("name/publish")
	if node != "" {
		req.Arguments(node)
	}
	req.Arguments(value)

	return req.Exec(context.Background(), &pubResp)
}

// PublishWithDetails is used for fine grained control over record publishing
func (s *Shell) PublishWithDetails(contentHash, key string, lifetime, ttl time.Duration, resolve bool) (*PublishResponse, error) {
	var pubResp PublishResponse
	req := s.Request("name/publish", contentHash).Option("resolve", resolve)
	if key != "" {
		req.Option("key", key)
	}
	if lifetime != 0 {
		req.Option("lifetime", lifetime)
	}
	if ttl.Seconds() > 0 {
		req.Option("ttl", ttl)
	}
	err := req.Exec(context.Background(), &pubResp)
	if err != nil {
		return nil, err
	}
	return &pubResp, nil
}

// Resolve gets resolves the string provided to an /ipns/[name]. If asked to
// resolve an empty string, resolve instead resolves the node's own /ipns value.
func (s *Shell) Resolve(id string) (string, error) {
	req := s.Request("name/resolve")
	if id != "" {
		req.Arguments(id)
	}
	var out struct{ Path string }
	err := req.Exec(context.Background(), &out)
	return out.Path, err
}
