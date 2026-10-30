package signing

import (
	"testing"

	policy "github.com/moby/policy-helpers"
	"github.com/moby/policy-helpers/types"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/stretchr/testify/require"
)

func TestSimplePatternMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		value   string
		match   bool
	}{
		{
			pattern: "",
			value:   "",
			match:   true,
		},
		{
			pattern: "builder",
			value:   "builder",
			match:   true,
		},
		{
			pattern: "builder",
			value:   "other-builder",
			match:   false,
		},
		{
			pattern: "*",
			value:   "",
			match:   true,
		},
		{
			pattern: "https://github.com/org/*",
			value:   "https://github.com/org/repo/workflow",
			match:   true,
		},
		{
			pattern: "https://github.com/org/*",
			value:   "https://github.com/other/repo",
			match:   false,
		},
		{
			pattern: "*@refs/heads/main",
			value:   "workflow@refs/heads/main",
			match:   true,
		},
		{
			pattern: "*@refs/heads/main",
			value:   "workflow@refs/heads/main-other",
			match:   false,
		},
		{
			pattern: "*workflow*",
			value:   "org/workflow@main",
			match:   true,
		},
		{
			pattern: "*workflow*",
			value:   "org/other@main",
			match:   false,
		},
		{
			pattern: "org/*/workflow",
			value:   "org/repo/workflow",
			match:   false,
		},
		{
			pattern: "org/*/workflow",
			value:   "org/*/workflow",
			match:   true,
		},
		{
			pattern: "*org/*/workflow*",
			value:   "org/repo/workflow@main",
			match:   false,
		},
	} {
		t.Run(tc.pattern+"/"+tc.value, func(t *testing.T) {
			require.Equal(t, tc.match, simplePatternMatch(tc.pattern, tc.value))
		})
	}
}

func TestVerifyPolicy(t *testing.T) {
	signer := &certificate.Summary{
		CertificateIssuer:      "https://issuer.example",
		SubjectAlternativeName: "https://github.com/org/repo/workflow@main",
		Extensions: certificate.Extensions{
			BuildSignerURI: "https://github.com/org/builder@main",
		},
	}
	for _, tc := range []struct {
		name       string
		policy     VerifyPolicy
		signer     *certificate.Summary
		timestamps []types.TimestampVerificationResult
		wantErr    string
	}{
		{
			name:    "missing signer",
			wantErr: "no signer found",
		},
		{
			name:   "unconstrained identity",
			signer: signer,
		},
		{
			name: "matching identity and extension",
			policy: VerifyPolicy{
				Summary: certificate.Summary{
					CertificateIssuer:      "https://issuer.example",
					SubjectAlternativeName: "https://github.com/org/*",
					Extensions: certificate.Extensions{
						BuildSignerURI: "*builder@main",
					},
				},
			},
			signer: signer,
		},
		{
			name: "wrong issuer",
			policy: VerifyPolicy{
				Summary: certificate.Summary{
					CertificateIssuer: "https://other.example",
				},
			},
			signer:  signer,
			wantErr: `certificate field "certificateIssuer" does not match policy`,
		},
		{
			name: "wrong workflow",
			policy: VerifyPolicy{
				Summary: certificate.Summary{
					SubjectAlternativeName: "https://github.com/trusted/*",
				},
			},
			signer:  signer,
			wantErr: `certificate field "subjectAlternativeName" does not match policy`,
		},
		{
			name: "missing required extension",
			policy: VerifyPolicy{
				Summary: certificate.Summary{
					Extensions: certificate.Extensions{
						SourceRepositoryURI: "https://github.com/org/repo",
					},
				},
			},
			signer:  signer,
			wantErr: `certificate field "sourceRepositoryURI" does not match policy`,
		},
		{
			name: "timestamp and log thresholds",
			policy: VerifyPolicy{
				TimestampThreshold: 2,
				TlogThreshold:      1,
			},
			signer: signer,
			timestamps: []types.TimestampVerificationResult{
				{
					Type: "Tlog",
				},
				{
					Type: "TimestampAuthority",
				},
			},
		},
		{
			name: "log also counts toward timestamp threshold",
			policy: VerifyPolicy{
				TimestampThreshold: 1,
				TlogThreshold:      1,
			},
			signer: signer,
			timestamps: []types.TimestampVerificationResult{
				{
					Type: "Tlog",
				},
			},
		},
		{
			name: "insufficient timestamps",
			policy: VerifyPolicy{
				TimestampThreshold: 1,
			},
			signer:  signer,
			wantErr: "not enough timestamp authorities: have 0, need 1",
		},
		{
			name: "timestamp authority is not a log",
			policy: VerifyPolicy{
				TlogThreshold: 1,
			},
			signer: signer,
			timestamps: []types.TimestampVerificationResult{
				{
					Type: "TimestampAuthority",
				},
			},
			wantErr: "not enough tlog authorities: have 0, need 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyPolicy(t.Context(), &types.SignatureInfo{
				Signer:     tc.signer,
				Timestamps: tc.timestamps,
			}, tc.policy)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVerifierProvider(t *testing.T) {
	providerErr := errors.New("trust unavailable")
	for _, tc := range []struct {
		name     string
		provider VerifierProvider
		wantErr  string
	}{
		{
			name:    "missing provider",
			wantErr: "no verifier available",
		},
		{
			name: "missing verifier",
			provider: func() (*policy.Verifier, error) {
				return nil, nil
			},
			wantErr: "no verifier available",
		},
		{
			name: "provider failure",
			provider: func() (*policy.Verifier, error) {
				return nil, providerErr
			},
			wantErr: providerErr.Error(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := NewVerifier(VerifyPolicy{}, tc.provider)
			err := v.Verify(t.Context(), digest.FromString("payload"), nil)
			require.ErrorContains(t, err, tc.wantErr)
			if tc.name == "provider failure" {
				require.ErrorIs(t, err, providerErr)
			}
		})
	}
}

func TestVerifierRejectsMalformedBundle(t *testing.T) {
	calls := 0
	v := NewVerifier(VerifyPolicy{}, func() (*policy.Verifier, error) {
		calls++
		// Malformed bundles are rejected before loading trust material.
		return &policy.Verifier{}, nil
	})
	require.Zero(t, calls, "constructing a verifier must not initialize trust")
	require.Error(t, v.Verify(t.Context(), digest.FromString("payload"), []byte("not a bundle")))
	require.Equal(t, 1, calls)
}
