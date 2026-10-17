//go:build cgo && (linux || darwin)

package sqlitecachestorage

import (
	"database/sql"
	"testing"

	"github.com/moby/buildkit/util/sql/template"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type keyValue[K, V any] struct {
	Key   K
	Value V
}

func TestCompile(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)

	err = Migrate(t.Context(), db)
	require.NoError(t, err)

	// Static queries. These may contain arguments since prepare
	// will just validate the syntax is correct.
	for _, tc := range []keyValue[string, string]{
		{"insertLinkSQL", insertLinkSQL},
		{"insertRecordSQL", insertRecordSQL},
		{"linkExistsSQL", linkExistsSQL},
		{"selectRecordsSQL", selectRecordsSQL},
		{"selectRecordSQL", selectRecordSQL},
		{"queryBacklinksSQL", queryBacklinksSQL},
		{"queryAlternativeRootsSQL", queryAlternativeRootsSQL},
		{"queryDistinctRecordsSQL", queryDistinctRecordsSQL},
		{"deleteUnreferencedLinksSQL", deleteUnreferencedLinksSQL},
	} {
		stmt, err := db.PrepareContext(t.Context(), tc.Value)
		assert.NoError(t, err, "%s", tc.Key)
		if stmt != nil {
			_ = stmt.Close()
		}
	}

	// Dynamic queries that take in a template.
	for _, tc := range []keyValue[func() *template.Template, any]{
		{queryLinksSQL, map[string]any{
			"Deps": []map[string]any{
				{
					"CacheKey": map[string]any{
						"ID": "foo",
					},
					"Selector": "def",
				},
			},
			"Digest": "abc",
			"Output": 0,
			"Input":  0,
		}},
		{deleteRecordsSQL, []string{"a", "b", "c"}},
	} {
		tmpl := tc.Key()
		stmt, _, err := tmpl.PrepareContext(t.Context(), db, tc.Value)
		assert.NoError(t, err, "%s", tmpl.Name())
		if stmt != nil {
			_ = stmt.Close()
		}
	}
}
