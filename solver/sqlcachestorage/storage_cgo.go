//go:build cgo && (linux || darwin)

package sqlcachestorage

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "github.com/mattn/go-sqlite3" // sqlite3 driver
)

func sqliteOpen(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite3", dsn(dbPath))
}

func dsn(dbPath string) string {
	params := url.Values{
		"_journal_mode": []string{"WAL"},
	}
	return fmt.Sprintf("file:%s?%s", dbPath, params.Encode())
}
