package sourcepolicy

import (
	"context"
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestEngineEvaluate(t *testing.T) {
	t.Run("Deny All", testDenyAll)
	t.Run("Allow Deny", testAllowDeny)
	t.Run("Convert", testConvert)
	t.Run("Convert Deny", testConvertDeny)
	t.Run("Allow Convert Deny", testAllowConvertDeny)
	t.Run("Test convert loop", testConvertLoop)
	t.Run("Test convert http", testConvertHTTP)
	t.Run("Test convert regex", testConvertRegex)
	t.Run("Test convert wildcard", testConvertWildcard)
	t.Run("Test convert multiple", testConvertMultiple)
}

func testConvertMultiple(t *testing.T) {
	pol := &spb.Policy{
		Rules: []*spb.Rule{
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/busybox:latest",
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://docker.io/library/alpine:latest",
				},
			},
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/alpine:latest",
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://docker.io/library/debian:buster",
				},
			},
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/debian:buster",
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://docker.io/library/debian:bullseye",
				},
			},
		},
	}

	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}

	ctx := context.Background()
	e := NewEngine(pol, nil, nil)

	mutated, err := e.Evaluate(ctx, op)
	require.True(t, mutated)
	require.NoError(t, err)
}

func testConvertWildcard(t *testing.T) {
	pol := &spb.Policy{
		Rules: []*spb.Rule{
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: `docker.io/library/golang:*`,
					MatchType:  spb.MatchType_WILDCARD,
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://fakereg.io/library/golang:${1}",
				},
			},
		},
	}

	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/golang:1.19",
			},
		},
	}

	ctx := context.Background()
	e := NewEngine(pol, nil, nil)

	mutated, err := e.Evaluate(ctx, op)
	require.True(t, mutated)
	require.NoError(t, err)
	require.Equal(t, "docker-image://fakereg.io/library/golang:1.19", op.GetSource().Identifier)
}

func testConvertRegex(t *testing.T) {
	pol := &spb.Policy{
		Rules: []*spb.Rule{
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: `docker\.io/library/golang:(.*)`,
					MatchType:  spb.MatchType_REGEX,
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://fakereg.io/library/golang:${1}",
				},
			},
		},
	}

	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/golang:1.19",
			},
		},
	}

	ctx := context.Background()
	e := NewEngine(pol, nil, nil)

	mutated, err := e.Evaluate(ctx, op)
	require.True(t, mutated)
	require.NoError(t, err)
	require.Equal(t, "docker-image://fakereg.io/library/golang:1.19", op.GetSource().Identifier)
}

func testConvertHTTP(t *testing.T) {
	pol := &spb.Policy{
		Rules: []*spb.Rule{
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "http",
					Identifier: "https://example.com/foo",
				},
				Destination: &spb.Destination{
					Attrs: map[string]string{"http.checksum": "sha256:1234"},
				},
			},
		},
	}

	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "https://example.com/foo",
			},
		},
	}

	ctx := context.Background()
	e := NewEngine(pol, nil, nil)

	mutated, err := e.Evaluate(ctx, op)
	require.True(t, mutated)
	require.NoError(t, err)
	require.Equal(t, op.GetSource().Identifier, "https://example.com/foo")
}

func testConvertLoop(t *testing.T) {
	pol := &spb.Policy{
		Rules: []*spb.Rule{
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/busybox:latest",
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://docker.io/library/alpine:latest",
				},
			},
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/alpine:latest",
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://docker.io/library/busybox:latest",
				},
			},
		},
	}

	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}

	ctx := context.Background()
	e := NewEngine(pol, nil, nil)

	mutated, err := e.Evaluate(ctx, op)
	require.True(t, mutated)
	require.ErrorIs(t, err, ErrTooManyOps)
}

func testAllowConvertDeny(t *testing.T) {
	pol := &spb.Policy{
		Rules: []*spb.Rule{
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/busybox:latest",
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://docker.io/library/alpine:latest",
				},
			},
			{
				Action: spb.PolicyAction_ALLOW,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/alpine:latest",
				},
			},
			{
				Action: spb.PolicyAction_DENY,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/alpine:*",
				},
			},
			{
				Action: spb.PolicyAction_DENY,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/busybox:latest",
				},
			},
		},
	}

	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}

	ctx := context.Background()
	e := NewEngine(pol, nil, nil)

	mutated, err := e.Evaluate(ctx, op)
	require.True(t, mutated)
	require.NoError(t, err)
	require.Equal(t, op.GetSource().Identifier, "docker-image://docker.io/library/alpine:latest")
}

func testConvertDeny(t *testing.T) {
	pol := &spb.Policy{
		Rules: []*spb.Rule{
			{
				Action: spb.PolicyAction_DENY,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/alpine:*",
				},
			},
			{
				Action: spb.PolicyAction_CONVERT,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/busybox:latest",
				},
				Destination: &spb.Destination{
					Identifier: "docker-image://docker.io/library/alpine:latest",
				},
			},
		},
	}

	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}

	ctx := context.Background()
	e := NewEngine(pol, nil, nil)

	mutated, err := e.Evaluate(ctx, op)
	require.True(t, mutated)
	require.ErrorIs(t, err, ErrSourceDenied)
	require.Equal(t, op.GetSource().Identifier, "docker-image://docker.io/library/alpine:latest")
}

func testConvert(t *testing.T) {
	cases := map[string]string{
		"docker-image://docker.io/library/busybox:latest": "docker-image://docker.io/library/alpine:latest",
		"docker-image://docker.io/library/alpine:latest":  "docker-image://docker.io/library/alpine:latest@sha256:c0d488a800e4127c334ad20d61d7bc21b4097540327217dfab52262adc02380c",
	}
	bklog.L.Logger.SetLevel(logrus.DebugLevel)

	for src, dst := range cases {
		t.Run(src+"=>"+dst, func(t *testing.T) {
			op := &pb.Op{
				Op: &pb.Op_Source{
					Source: &pb.SourceOp{
						Identifier: src,
					},
				},
			}

			kind, ref, _ := strings.Cut(src, "://")
			pol := &spb.Policy{
				Rules: []*spb.Rule{
					{
						Action: spb.PolicyAction_CONVERT,
						Source: &spb.Source{
							Type:       kind,
							Identifier: ref,
						},
						Destination: &spb.Destination{
							Identifier: dst,
						},
					},
				},
			}

			ctx := context.Background()
			e := NewEngine(pol, nil, nil)

			mutated, err := e.Evaluate(ctx, op)
			require.True(t, mutated)
			require.NoError(t, err)
			require.Equal(t, dst, op.GetSource().Identifier)
		})
	}
}

func testAllowDeny(t *testing.T) {
	op := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/alpine:latest",
			},
		},
	}
	pol := &spb.Policy{
		Rules: []*spb.Rule{
			{
				Action: spb.PolicyAction_ALLOW,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "docker.io/library/alpine:latest",
				},
			},
			{
				Action: spb.PolicyAction_DENY,
				Source: &spb.Source{
					Type:       "docker-image",
					Identifier: "*",
				},
			},
		},
	}

	ctx := context.Background()
	e := NewEngine(pol, nil, nil)

	mutated, err := e.Evaluate(ctx, op)
	require.False(t, mutated)
	require.NoError(t, err)

	op = &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}

	mutated, err = e.Evaluate(ctx, op)
	require.False(t, mutated)
	require.ErrorIs(t, err, ErrSourceDenied)
}

func testDenyAll(t *testing.T) {
	cases := map[string]string{
		"docker-image": "docker.io/library/alpine:latest",
		"git":          "https://github.com/moby/buildkit.git",
		"http":         "https://example.com",
	}

	for kind, ref := range cases {
		t.Run(kind+"://"+ref, func(t *testing.T) {
			pol := &spb.Policy{
				Rules: []*spb.Rule{
					{
						Action: spb.PolicyAction_DENY,
						Source: &spb.Source{
							Type:       kind,
							Identifier: "*",
						},
					},
				},
			}

			e := NewEngine(pol, nil, nil)
			ctx := context.Background()

			op := &pb.Op{
				Op: &pb.Op_Source{
					Source: &pb.SourceOp{
						Identifier: kind + "://" + ref,
					},
				},
			}

			mutated, err := e.Evaluate(ctx, op)
			require.False(t, mutated)
			require.ErrorIs(t, err, ErrSourceDenied)
		})
	}
}
