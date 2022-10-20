package oci

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

type OciHook struct {
	Phase   string
	Path    string
	Args    []string
	Env     []string
	Timeout *int
}

func WithHook(hook OciHook) oci.SpecOpts {
	return func(ctx context.Context, client oci.Client, c *containers.Container, s *oci.Spec) error {
		if s.Hooks == nil {
			s.Hooks = &specs.Hooks{}
		}

		h := specs.Hook{
			Path:    hook.Path,
			Args:    hook.Args,
			Env:     hook.Args,
			Timeout: hook.Timeout,
		}

		// Yes, its verbose... but it reads _so much better_ than the golang reflection version
		switch hook.Phase {
		case "prestart":
			s.Hooks.Prestart = append(s.Hooks.Prestart, h)
		case "createRuntime":
			s.Hooks.CreateRuntime = append(s.Hooks.CreateRuntime, h)
		case "createContainer":
			s.Hooks.CreateContainer = append(s.Hooks.CreateContainer, h)
		case "startContainer":
			s.Hooks.StartContainer = append(s.Hooks.StartContainer, h)
		case "poststart":
			s.Hooks.Poststart = append(s.Hooks.Poststart, h)
		case "poststop":
			s.Hooks.Poststop = append(s.Hooks.Poststop, h)
		default:
			return fmt.Errorf("%s is not a valid lifecycle hook", hook.Phase)
		}

		return nil
	}
}
