package linter

import (
	"fmt"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

type LinterRule[F any] struct {
	Name        string
	Description string
	URL         string
	Format      F
}

func (rule LinterRule[F]) Run(warn LintWarnFunc, location []parser.Range, txt ...string) {
	startLine := 0
	if len(location) > 0 {
		startLine = location[0].Start.Line
	}
	if len(txt) == 0 {
		txt = []string{rule.Description}
	}
	short := LintFormatShort(rule.Name, strings.Join(txt, " "), startLine)
	warn(rule.Name, rule.Description, rule.URL, short, location)
}

func LintFormatShort(rulename, msg string, startLine int) string {
	return fmt.Sprintf("Lint Rule '%s': %s (line %d)", rulename, msg, startLine)
}

type LintWarnFunc func(rulename, description, url, fmtmsg string, location []parser.Range)
