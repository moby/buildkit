//go:build cgo && (linux || darwin)

package sqlitecachestorage

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "github.com/mattn/go-sqlite3" // include import here since this controls which open function is used
)

func sqliteOpen(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dsn(dbPath))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func dsn(dbPath string) string {
	params := url.Values{
		"_journal_mode": []string{"WAL"},
		"_foreign_keys": []string{"on"},
	}
	return fmt.Sprintf("file:%s?%s", dbPath, params.Encode())
}
