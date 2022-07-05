package sourcepolicyllbmutator

import (
	"context"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	"github.com/stretchr/testify/require"
)

func TestLLBMutator(t *testing.T) {
	type testCaseOp struct {
		op          *pb.Op
		expected    bool
		expectedOp  *pb.Op
		expectedErr string
	}

	type testCasePol struct {
		pol       *sourcepolicy.SourcePolicy
		testCases []testCaseOp
	}
	testCases := []testCasePol{
		{
			pol: &sourcepolicy.SourcePolicy{
				Sources: []sourcepolicy.Source{
					{
						Type: "docker-image",
						Ref:  "docker.io/library/busybox:1.34.1-uclibc",
						Pin:  "sha256:3614ca5eacf0a3a1bcc361c939202a974b4902b9334ff36eb29ffe9011aaad83",
					},
					{
						Type: "docker-image",
						Ref:  "docker.io/library/busybox:latest",
						Pin:  "sha256:3614ca5eacf0a3a1bcc361c939202a974b4902b9334ff36eb29ffe9011aaad83",
					},
					{
						Type: "http",
						Ref:  "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
						Pin:  "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53",
					},
				},
			},
			testCases: []testCaseOp{
				{
					op: &pb.Op{
						Op: &pb.Op_Source{
							Source: &pb.SourceOp{
								Identifier: "docker-image://docker.io/library/busybox:1.34.1-uclibc",
							},
						},
					},
					expected: true,
					expectedOp: &pb.Op{
						Op: &pb.Op_Source{
							Source: &pb.SourceOp{
								Identifier: "docker-image://docker.io/library/busybox:1.34.1-uclibc@sha256:3614ca5eacf0a3a1bcc361c939202a974b4902b9334ff36eb29ffe9011aaad83",
							},
						},
					},
				},
				{
					op: &pb.Op{
						Op: &pb.Op_Source{
							Source: &pb.SourceOp{
								Identifier: "docker-image://docker.io/library/busybox",
							},
						},
					},
					expected: true,
					expectedOp: &pb.Op{
						Op: &pb.Op_Source{
							Source: &pb.SourceOp{
								Identifier: "docker-image://docker.io/library/busybox:latest@sha256:3614ca5eacf0a3a1bcc361c939202a974b4902b9334ff36eb29ffe9011aaad83",
							},
						},
					},
				},
				{
					// Discard the existing digest that might have been resolved by the Dockerfile frontend's MetaResolver.
					op: &pb.Op{
						Op: &pb.Op_Source{
							Source: &pb.SourceOp{
								Identifier: "docker-image://docker.io/library/busybox:latest@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
							},
						},
					},
					expected: true,
					expectedOp: &pb.Op{
						Op: &pb.Op_Source{
							Source: &pb.SourceOp{
								Identifier: "docker-image://docker.io/library/busybox:latest@sha256:3614ca5eacf0a3a1bcc361c939202a974b4902b9334ff36eb29ffe9011aaad83",
							},
						},
					},
				},
				{
					op: &pb.Op{
						Op: &pb.Op_Source{
							Source: &pb.SourceOp{
								Identifier: "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
							},
						},
					},
					expected: true,
					expectedOp: &pb.Op{
						Op: &pb.Op_Source{
							Source: &pb.SourceOp{
								Identifier: "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
								Attrs: map[string]string{
									pb.AttrHTTPChecksum: "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53",
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tcPol := range testCases {
		ctx := context.TODO()
		lm, err := New(tcPol.pol)
		require.NoError(t, err)
		for _, tcOp := range tcPol.testCases {
			op := *tcOp.op
			mutated, err := lm.Mutate(ctx, &op)
			if tcOp.expectedErr != "" {
				require.Error(t, err, tcOp.expectedErr)
			} else {
				require.Equal(t, tcOp.expected, mutated)
				require.Equal(t, tcOp.expectedOp, &op)
			}
		}
	}
}
