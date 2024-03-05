package linter

import (
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

const (
	RuleStageNameCasing = "StageNameCasing"
	RuleFlagCasing      = "FlagCasing"
	RuleCommandCasing   = "CommandCasing"
)

var LinterRules map[string]LinterRule

type LinterRule struct {
	Short       string
	Description string
}

type Warning struct {
	Short    string       `json:"short"`
	Detail   [][]byte     `json:"detail"`
	Location parser.Range `json:"location"`
	Source   string       `json:"source"`
}

func init() {
	LinterRules = map[string]LinterRule{
		RuleStageNameCasing: {
			Short:       "Lowercase Staging Name",
			Description: "Stage names should be lowercase",
		},
		RuleFlagCasing: {
			Short:       "Lowercase Command Flags",
			Description: "Flags should be lowercase",
		},
		RuleCommandCasing: {
			Short:       "Inconsistent Command Casing",
			Description: "Commands should be in consistent casing (all lower or all upper)",
		},
	}
}

func isUpperCase(s string) bool {
	return s == strings.ToUpper(s)
}

func isLowerCase(s string) bool {
	return s == strings.ToLower(s)
}

func consistentCasing(s string) bool {
	return isUpperCase(s) || isLowerCase(s)
}

func ValidateStageNameCasing(val interface{}) bool {
	cmdArgs, ok := val.([]string)
	if !ok {
		return false
	}
	if len(cmdArgs) != 3 {
		return true
	}
	stageName := cmdArgs[2]
	return isLowerCase(stageName)
}

func ValidateFlagCasing(val interface{}) bool {
	flags, ok := val.([]string)
	if !ok {
		return false
	}
	for _, flag := range flags {
		if !isLowerCase(flag) {
			return false
		}
	}
	return true
}

func ValidateCommandCasing(val interface{}) bool {
	cmd, ok := val.(string)
	return ok && consistentCasing(cmd)
}

func ValidateNodeLintRules(node *parser.Node) []parser.Warning {
	warnings := []parser.Warning{}
	location := FirstNodeRange(node)
	if !ValidateFlagCasing(node.Flags) {
		flagCasing := LinterRules[RuleFlagCasing]
		warnings = append(warnings, parser.Warning{
			Short:    flagCasing.Short,
			Detail:   [][]byte{[]byte(flagCasing.Description)},
			Location: location,
		})
	}
	if !ValidateCommandCasing(node.Value) {
		commandCasing := LinterRules[RuleCommandCasing]
		warnings = append(warnings, parser.Warning{
			Short:    commandCasing.Short,
			Detail:   [][]byte{[]byte(commandCasing.Description)},
			Location: location,
		})
	}
	return warnings
}

func FirstNodeRange(node *parser.Node) *parser.Range {
	if len(node.Location()) > 0 {
		return &node.Location()[0]
	}
	return &parser.Range{
		Start: parser.Position{Line: node.StartLine},
		End:   parser.Position{Line: node.EndLine},
	}
}
