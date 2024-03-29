package network

import (
	"context"

	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func NewNoneProvider() Provider {
	return &none{}
}

type none struct {
}

func (h *none) New(_ context.Context, hostname string) (Namespace, error) {
	return &noneNS{}, nil
}

func (h *none) Close() error {
	return nil
}

type noneNS struct {
}

func (h *noneNS) Set(s *specs.Spec) error {
	return nil
}

func (h *noneNS) Close() error {
	return nil
}

func (h *noneNS) Sample() (*resourcestypes.NetworkSample, error) {
	return nil, nil
}
