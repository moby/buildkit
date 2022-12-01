package sourcepolicy

import (
	"context"
	"testing"

	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/stretchr/testify/require"
)

func TestMatch(t *testing.T) {
	type testCase struct {
		name    string
		src     spb.Source
		scheme  string
		ref     string
		attrs   map[string]string
		matches bool
		xErr    bool
	}

	cases := []testCase{
		{
			name: "basic exact match",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:1.34.1-uclibc",
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:1.34.1-uclibc",
			matches: true,
		},
		{
			name: "docker-image scheme matches with only wildcard",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "*",
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: true,
		},
		{
			name: "docker-image scheme matches with wildcard",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: true,
		},
		{
			name: "mis-matching scheme does not match",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
			},
			scheme:  "http",
			ref:     "http://docker.io/library/busybox:latest",
			matches: false,
		},
		{
			name: "http scheme matches with wildcard",
			src: spb.Source{
				Type:       "http",
				Identifier: "http://docker.io/library/busybox:*",
			},
			scheme:  "http",
			ref:     "http://docker.io/library/busybox:latest",
			matches: true,
		},
		{
			name: "http scheme matches https URL",
			src: spb.Source{
				Type:       "http",
				Identifier: "https://docker.io/library/busybox:*",
			},
			scheme:  "http",
			ref:     "https://docker.io/library/busybox:latest",
			matches: true,
		},
		{
			name: "attr match with default constraint (equals) matches",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:   "foo",
						Value: "bar",
						// Default equals
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: true,
			attrs:   map[string]string{"foo": "bar"},
		},
		{
			name: "attr match with default constraint (equals) does not match",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:   "foo",
						Value: "bar",
						// Default equals
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: false,
			attrs:   map[string]string{"foo": "nope"},
		},
		{
			name: "attr match with explicit equals matches",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "bar",
						Condition: spb.AttrMatch_EQUAL, // explicit equals
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: true,
			attrs:   map[string]string{"foo": "bar"},
		},
		{
			name: "attr match with explicit equals does not match",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "bar",
						Condition: spb.AttrMatch_EQUAL, // explicit equals
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: false,
			attrs:   map[string]string{"foo": "nope"},
		},
		{
			name: "attr match not equal does not match",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "bar",
						Condition: spb.AttrMatch_NOTEQUAL,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: false,
			attrs:   map[string]string{"foo": "bar"},
		},
		{
			name: "attr match not equal does match",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "bar",
						Condition: spb.AttrMatch_NOTEQUAL,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: true,
			attrs:   map[string]string{"foo": "ok"},
		},
		{
			name: "matching attach match with simple strings",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "bar",
						Condition: spb.AttrMatch_MATCHES,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: true,
			attrs:   map[string]string{"foo": "bar"},
		},
		{
			name: "non-matching attr match constraint simple strings",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "asdf",
						Condition: spb.AttrMatch_MATCHES,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: false,
			attrs:   map[string]string{"foo": "bar"},
		},
		{
			name: "complex regex attr match",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "^b\\w+",
						Condition: spb.AttrMatch_MATCHES,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: true,
			attrs:   map[string]string{"foo": "bar"},
		},
		{
			name: "attr constraint with non-matching regex",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "^\\d+",
						Condition: spb.AttrMatch_MATCHES,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			attrs:   map[string]string{"foo": "b1"},
			matches: false,
		},
		{
			name: "attr constraint with regex match",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "\\d$",
						Condition: spb.AttrMatch_MATCHES,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			attrs:   map[string]string{"foo": "b1"},
			matches: true,
		},
		{
			name: "unknown attr match condition type",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "^b+",
						Condition: -1, // unknown condition
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			attrs:   map[string]string{"foo": "b1"},
			matches: false,
			xErr:    true,
		},
		{
			name: "matching constraint with extra attrs",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "Foo",
						Condition: spb.AttrMatch_EQUAL,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			attrs:   map[string]string{"foo": "Foo", "bar": "Bar"},
			matches: true,
		},
		{
			name: "multiple attrs with failed constraint",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "Foo",
						Condition: spb.AttrMatch_EQUAL,
					},
					{
						Key:       "bar",
						Value:     "nope",
						Condition: spb.AttrMatch_EQUAL,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			attrs:   map[string]string{"foo": "Foo", "bar": "Bar"},
			matches: false,
		},
		{
			name: "non-existent constraint key",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "Foo",
						Condition: spb.AttrMatch_EQUAL,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			matches: false,
		},
		{
			name: "non-existent constraint key w/ non-nill attr",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:*",
				Constraints: []*spb.AttrConstraint{
					{
						Key:       "foo",
						Value:     "Foo",
						Condition: spb.AttrMatch_EQUAL,
					},
				},
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest",
			attrs:   map[string]string{"bar": "Bar"},
			matches: false,
		},
		{
			name: "strip docker-image digest from source op for matching",
			src: spb.Source{
				Type:       "docker-image",
				Identifier: "docker.io/library/busybox:latest",
			},
			scheme:  "docker-image",
			ref:     "docker.io/library/busybox:latest@sha256:f75f3d1a317fc82c793d567de94fc8df2bece37acd5f2bd364a0d91a0d1f3dab",
			matches: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			matches, err := Match(context.Background(), &Source{Source: &tc.src}, tc.scheme, tc.ref, tc.attrs)
			if !tc.xErr {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, tc.matches, matches)
		})
	}
}
