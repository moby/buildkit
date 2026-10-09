// Package template is a wrapper around text/template for the purposes of safely
// generating dynamic SQL queries. This adds some utilities for executing queries
// using the data to generate queries. The "bind" function can be used to safely
// insert a bound argument into the query.
package template

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	text "text/template"
)

type Template struct {
	inner *text.Template
}

func New(name string) *Template {
	return &Template{
		inner: text.New(name).Funcs(text.FuncMap{
			// This function will be overridden during execution but
			// needs to be present during parsing.
			"bind": func(arg any) string {
				return "?"
			},
		}),
	}
}

func (t *Template) Parse(text string) (*Template, error) {
	inner, err := t.inner.Parse(text)
	if err != nil {
		return nil, err
	}
	return &Template{inner}, nil
}

func Must(t *Template, err error) *Template {
	var inner *text.Template
	if t.inner != nil {
		inner = t.inner
	}
	return &Template{text.Must(inner, err)}
}

func (t *Template) QueryContext(ctx context.Context, db StatementPreparer, data any) (*sql.Rows, error) {
	return withStmt(ctx, t, db, data, func(stmt *sql.Stmt, args []any) (*sql.Rows, error) {
		return stmt.QueryContext(ctx, args...)
	})
}

func (t *Template) QueryRowContext(ctx context.Context, db StatementPreparer, data any) (*sql.Row, error) {
	return withStmt(ctx, t, db, data, func(stmt *sql.Stmt, args []any) (*sql.Row, error) {
		row := stmt.QueryRowContext(ctx, args...)
		return row, row.Err()
	})
}

func (t *Template) ExecContext(ctx context.Context, db StatementPreparer, data any) (sql.Result, error) {
	return withStmt(ctx, t, db, data, func(stmt *sql.Stmt, args []any) (sql.Result, error) {
		return stmt.ExecContext(ctx, args...)
	})
}

func withStmt[Result any](ctx context.Context, t *Template, db StatementPreparer, data any, fn func(*sql.Stmt, []any) (Result, error)) (res Result, _ error) {
	tmpl, err := t.inner.Clone()
	if err != nil {
		return res, err
	}

	var args []any
	tmpl = tmpl.Funcs(text.FuncMap{
		"bind": func(arg any) string {
			args = append(args, arg)
			// Note: Indices are 1 indexed and not 0 indexed.
			return fmt.Sprintf("?%d", len(args))
		},
	})

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return res, err
	}

	stmt, err := db.PrepareContext(ctx, sb.String())
	if err != nil {
		return res, err
	}
	defer stmt.Close()

	return fn(stmt, args)
}

type StatementPreparer interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}
