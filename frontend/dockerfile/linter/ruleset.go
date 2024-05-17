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
	RuleFromAsCasing = LinterRule[func(string, string) string]{
		Name:        "FromAsCasing",
		Description: "The 'as' keyword should match the case of the 'from' keyword",
		Format: func(from, as string) string {
			return fmt.Sprintf("'%s' and '%s' keywords' casing do not match", as, from)
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
	RuleSelfConsistentCommandCasing = LinterRule[func(string) string]{
		Name:        "SelfConsistentCommandCasing",
		Description: "Commands should be in consistent casing (all lower or all upper)",
		Format: func(command string) string {
			return fmt.Sprintf("Command '%s' should be consistently cased", command)
		},
	}
	RuleFileConsistentCommandCasing = LinterRule[func(string, string) string]{
		Name:        "FileConsistentCommandCasing",
		Description: "All commands within the Dockerfile should use the same casing (either upper or lower)",
		Format: func(violatingCommand, correctCasing string) string {
			return fmt.Sprintf("Command '%s' should match the case of the command majority (%s)", violatingCommand, correctCasing)
		},
	}
	RuleDuplicateStageName = LinterRule[func(string) string]{
		Name:        "DuplicateStageName",
		Description: "Stage names should be unique",
		Format: func(stageName string) string {
			return fmt.Sprintf("Duplicate stage name %q, stage names should be unique", stageName)
		},
	}
	RuleReservedStageName = LinterRule[func(string) string]{
		Name:        "ReservedStageName",
		Description: "Reserved stage names should not be used to name a stage",
		Format: func(reservedStageName string) string {
			return fmt.Sprintf("Stage name should not use the same name as reserved stage %q", reservedStageName)
		},
	}
	RuleJSONArgsRecommended = LinterRule[func(instructionName string) string]{
		Name:        "JSONArgsRecommended",
		Description: "JSON arguments recommended for ENTRYPOINT/CMD to prevent unintended behavior related to OS signals",
		Format: func(instructionName string) string {
			return fmt.Sprintf("JSON arguments recommended for %s to prevent unintended behavior related to OS signals", instructionName)
		},
	}
	RuleMaintainerDeprecated = LinterRule[func() string]{
		Name:        "MaintainerDeprecated",
		Description: "The maintainer instruction is deprecated, use a label instead to define an image author",
		URL:         "https://docs.docker.com/reference/dockerfile/#maintainer-deprecated",
		Format: func() string {
			return "Maintainer instruction is deprecated in favor of using label"
		},
	}
	RuleUndeclaredArgInFrom = LinterRule[func(string) string]{
		Name:        "UndeclaredArgInFrom",
		Description: "FROM command must use declared ARGs",
		Format: func(baseArg string) string {
			return fmt.Sprintf("FROM argument '%s' is not declared", baseArg)
		},
	}
	RuleWorkdirRelativePath = LinterRule[func(workdir string) string]{
		Name:        "WorkdirRelativePath",
		Description: "Relative workdir without an absolute workdir declared within the build can have unexpected results if the base image changes",
		Format: func(workdir string) string {
			return fmt.Sprintf("Relative workdir %q can have unexpected results if the base image changes", workdir)
		},
	}
	RuleUndefinedArg = LinterRule[func(string) string]{
		Name:        "UndefinedArg",
		Description: "ARGs should be defined before their use",
		Format: func(arg string) string {
			return fmt.Sprintf("Usage of undefined variable '$%s'", arg)
		},
	}
	RuleUndefinedVar = LinterRule[func(string) string]{
		Name:        "UndefinedVar",
		Description: "Variables should be defined before their use",
		Format: func(arg string) string {
			return fmt.Sprintf("Usage of undefined variable '$%s'", arg)
		},
	}
	RuleMultipleInstructionsDisallowed = LinterRule[func(instructionName string) string]{
		Name:        "MultipleInstructionsDisallowed",
		Description: "Multiple instructions of the same type should not be used in the same stage",
		Format: func(instructionName string) string {
			return fmt.Sprintf("Multiple %s instructions should not be used in the same stage because only the last one will be used", instructionName)
		},
	}
	RuleOutOfScopeVar = LinterRule[func(string, string) string]{
		Name:        "OutOfScopeVar",
		Description: "Variables should be defined within the same stage",
		Format: func(word, stageName string) string {
			return fmt.Sprintf("Variable '%s' is out of scope for stage '%s', it should be defined within the same stage", word, stageName)
		},
	}
)
