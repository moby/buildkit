//go:build cgo && (linux || darwin)

package sqlitecachestorage

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigrate tests various migration paths.
func TestMigrate(t *testing.T) {
	for _, path := range [][]string{
		{"v1.0"},
	} {
		t.Run(strings.Join(path, ">"), func(t *testing.T) {
			db, err := sql.Open("sqlite3", ":memory:")
			require.NoError(t, err)

			for _, desired := range path {
				err = migrate(t.Context(), db, desired)
				require.NoError(t, err)
			}

			final := path[len(path)-1]
			ver, err := currentVersion(t.Context(), db)
			require.NoError(t, err)
			require.Equal(t, final, ver)
		})
	}
}
