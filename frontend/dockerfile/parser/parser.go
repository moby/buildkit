// The parser package implements the official high-level Dockerfile parser.
//
// The parser extracts the separate stages from the dockerfile, as well as a
// collection of warnings, bundling them into a Result.
package parser

import (
	"io"

	"github.com/moby/buildkit/frontend/dockerfile/parser/ast"
	"github.com/moby/buildkit/frontend/dockerfile/parser/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
)

type Result struct {
	Stages   []instructions.Stage      // List of stages in the Dockerfile
	MetaArgs []instructions.ArgCommand // Arguments in the Dockerfile that occur before a stage

	Warnings []ast.Warning // Warnings collected during parsing

	Shlex *shell.Lex // Shell lexer with the same parameters used parsing
}

// Parse a Dockerfile from a Reader and produce a Result if successful.
func Parse(r io.Reader) (*Result, error) {
	astResult, err := ast.Parse(r)
	if err != nil {
		return nil, err
	}

	instResult, err := instructions.Parse(astResult.AST)
	if err != nil {
		return nil, err
	}

	return &Result{
		Warnings: astResult.Warnings,
		Stages:   instResult.Stages,
		MetaArgs: instResult.MetaArgs,
		Shlex:    shell.NewLex(astResult.EscapeToken),
	}, nil
}
