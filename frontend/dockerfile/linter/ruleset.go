package linter

import (
	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

var (
	RuleStageNameCasing LinterRule = LinterRule{
		Name:        "StageNameCasing",
		Description: "Stage names should be lowercase",
	}
	RuleNoEmptyContinuations LinterRule = LinterRule{
		Name:        "NoEmptyContinuations",
		Description: "Empty continuation lines will become errors in a future release",
		URL:         "https://github.com/moby/moby/pull/33719",
	}
	RuleCommandCasing LinterRule = LinterRule{
		Name:        "CommandCasing",
		Description: "Commands should be in consistent casing (all lower or all upper)",
	}
)

type LinterRule struct {
	Name        string
	URL         string
	Description string
}

type LintWarnFunc func(rule LinterRule, message string, location []parser.Range)
