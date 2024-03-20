package linter

import (
	"fmt"
)

var (
	RuleStageNameCasing = LinterRule[func(string) string]{
		Name:        "StageNameCasing",
		Description: "Stage names should be lowercase",
		Format: func(stageName string) string {
			return fmt.Sprintf("Stage name '%s' should be lowercase", stageName)
		},
	}
	RuleNoEmptyContinuations = LinterRule[func() string]{
		Name:        "NoEmptyContinuations",
		Description: "Empty continuation lines will become errors in a future release",
		URL:         "https://github.com/moby/moby/pull/33719",
		Format: func() string {
			return "Empty continuation line"
		},
	}
	RuleCommandCasing = LinterRule[func(string) string]{
		Name:        "CommandCasing",
		Description: "Commands should be in consistent casing (all lower or all upper)",
		Format: func(command string) string {
			return fmt.Sprintf("Command '%s' should be consistently cased", command)
		},
	}
)
