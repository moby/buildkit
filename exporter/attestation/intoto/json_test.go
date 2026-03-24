package intoto

import (
	"encoding/json"
	"strings"
	"testing"

	attestationv1 "github.com/in-toto/attestation/go/v1"
	legacyintoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/stretchr/testify/require"
)

func TestStatementMarshalJSON(t *testing.T) {
	t.Parallel()

	stmt := Statement{
		Type: legacyintoto.StatementInTotoV01,
		Subject: []Subject{{
			Name: "pkg:docker/example@sha256:abc",
			Digest: map[string]string{
				"sha256": strings.Repeat("a", 64),
			},
		}},
		PredicateType: "https://example.com/attestations/v1.0",
		Predicate:     json.RawMessage(`{"success":true}`),
	}

	expected := `{
		"_type":"https://in-toto.io/Statement/v1",
		"subject":[{"name":"pkg:docker/example@sha256:abc","digest":{"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}],
		"predicateType":"https://example.com/attestations/v1.0",
		"predicate":{"success":true}
	}`

	for _, tc := range []struct {
		name string
		v    any
	}{
		{"value", stmt},
		{"pointer", &stmt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dt, err := json.Marshal(tc.v)
			require.NoError(t, err)
			require.JSONEq(t, expected, string(dt))
		})
	}
}

func TestStatementMarshalJSONOmitsEmptyPredicate(t *testing.T) {
	t.Parallel()

	stmt := Statement{
		Subject: []Subject{{
			Name: "artifact",
			Digest: map[string]string{
				"sha256": strings.Repeat("b", 64),
			},
		}},
		PredicateType: "https://example.com/attestations/v1.0",
	}

	dt, err := json.Marshal(&stmt)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"_type":"https://in-toto.io/Statement/v1",
		"subject":[{"name":"artifact","digest":{"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}],
		"predicateType":"https://example.com/attestations/v1.0"
	}`, string(dt))
}

func TestStatementUnmarshalJSONLegacyV01(t *testing.T) {
	t.Parallel()

	var stmt Statement
	err := json.Unmarshal([]byte(`{
		"_type":"https://in-toto.io/Statement/v0.1",
		"subject":[{"name":"artifact","digest":{"sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}],
		"predicateType":"https://example.com/attestations/v1.0",
		"predicate":{"success":true}
	}`), &stmt)
	require.NoError(t, err)

	require.Equal(t, legacyintoto.StatementInTotoV01, stmt.Type)
	require.Equal(t, []Subject{{
		Name: "artifact",
		Digest: map[string]string{
			"sha256": strings.Repeat("c", 64),
		},
	}}, stmt.Subject)
	require.Equal(t, "https://example.com/attestations/v1.0", stmt.PredicateType)
	require.JSONEq(t, `{"success":true}`, string(stmt.Predicate))
}

func TestStatementUnmarshalJSONV1(t *testing.T) {
	t.Parallel()

	var stmt Statement
	err := json.Unmarshal([]byte(`{
		"_type":"https://in-toto.io/Statement/v1",
		"subject":[{"name":"artifact","digest":{"sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}],
		"predicateType":"https://example.com/attestations/v1.0",
		"predicate":{"success":true}
	}`), &stmt)
	require.NoError(t, err)

	require.Equal(t, attestationv1.StatementTypeUri, stmt.Type)
	require.Equal(t, []Subject{{
		Name: "artifact",
		Digest: map[string]string{
			"sha256": strings.Repeat("d", 64),
		},
	}}, stmt.Subject)
	require.Equal(t, "https://example.com/attestations/v1.0", stmt.PredicateType)
	require.JSONEq(t, `{"success":true}`, string(stmt.Predicate))
}

func TestStatementUnmarshalJSONNullPredicate(t *testing.T) {
	t.Parallel()

	var stmt Statement
	err := json.Unmarshal([]byte(`{
		"_type":"https://in-toto.io/Statement/v0.1",
		"subject":[{"name":"artifact","digest":{"sha256":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}],
		"predicateType":"https://example.com/attestations/v1.0",
		"predicate":null
	}`), &stmt)
	require.NoError(t, err)
	require.Nil(t, stmt.Predicate)
}

func TestStatementUnmarshalJSONOmittedPredicate(t *testing.T) {
	t.Parallel()

	var stmt Statement
	err := json.Unmarshal([]byte(`{
		"_type":"https://in-toto.io/Statement/v1",
		"subject":[{"name":"artifact","digest":{"sha256":"abababababababababababababababababababababababababababababababab"}}],
		"predicateType":"https://example.com/attestations/v1.0"
	}`), &stmt)
	require.NoError(t, err)
	require.Nil(t, stmt.Predicate)
}

func TestStatementUnmarshalJSONRejectsUnknownType(t *testing.T) {
	t.Parallel()

	var stmt Statement
	err := json.Unmarshal([]byte(`{
		"_type":"https://in-toto.io/Statement/v9",
		"subject":[{"name":"artifact","digest":{"sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}}],
		"predicateType":"https://example.com/attestations/v1.0",
		"predicate":{"success":true}
	}`), &stmt)
	require.Error(t, err)
}
