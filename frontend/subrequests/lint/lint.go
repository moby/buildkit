package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
)

const RequestLint = "frontend.lint"

var SubrequestLintDefinition = subrequests.Request{
	Name:        RequestLint,
	Version:     "1.0.0",
	Type:        subrequests.TypeRPC,
	Description: "Lint a Dockerfile",
	Opts:        []subrequests.Named{},
	Metadata: []subrequests.Named{
		{Name: "result.json"},
		{Name: "result.txt"},
	},
}

type Warning struct {
	RuleName  string   `json:"rule_name"`
	Detail    string   `json:"detail"`
	Filename  string   `json:"filename"`
	Source    []string `json:"source"`
	StartLine int      `json:"start_line"`
}

type LintResults struct {
	Warnings []Warning `json:"warnings"`
}

func (warns LintResults) ToResult() (*client.Result, error) {
	res := client.NewResult()
	dt, err := json.MarshalIndent(warns, "", "  ")
	if err != nil {
		return nil, err
	}
	res.AddMeta("result.json", dt)

	b := bytes.NewBuffer(nil)
	if err := PrintLintViolations(dt, b); err != nil {
		return nil, err
	}
	res.AddMeta("result.txt", b.Bytes())

	res.AddMeta("version", []byte(SubrequestLintDefinition.Version))
	return res, nil
}

func PrintLintViolations(dt []byte, w io.Writer) error {
	var warnings LintResults

	if err := json.Unmarshal(dt, &warnings); err != nil {
		return err
	}

	// Group warnings by start line
	lintWarnings := make(map[string][]Warning)
	lintWarningRules := []string{}
	for _, warning := range warnings.Warnings {
		if _, ok := lintWarnings[warning.RuleName]; !ok {
			lintWarningRules = append(lintWarningRules, warning.RuleName)
			lintWarnings[warning.RuleName] = []Warning{}
		}
		lintWarnings[warning.RuleName] = append(lintWarnings[warning.RuleName], warning)
	}
	sort.Strings(lintWarningRules)

	// Sort Warnings by filename and start line
	//sort.Slice(warnings.Warnings, func(i, j int) bool {
	//	if warnings.Warnings[i].Filename == warnings.Warnings[j].Filename {
	//		return warnings.Warnings[i].StartLine < warnings.Warnings[j].StartLine
	//	}
	//	return warnings.Warnings[i].Filename < warnings.Warnings[j].Filename
	//})

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	// #TODO print the warnings in a human readable format
	//for _, warning := range warnings.Warnings {
	//	fmt.Fprintf(tw, "%s:%d\n\t%s\n", warning.Filename, warning.StartLine, warning.Short)
	//}
	for _, rule := range lintWarningRules {
		fmt.Fprintf(tw, "Lint Rule %s\n", rule)
		for _, warning := range lintWarnings[rule] {
			fmt.Fprintf(tw, "\t%s:%d\n", warning.Filename, warning.StartLine)
			fmt.Fprintf(tw, "\t%s\n", warning.Detail)
			for offset, source := range warning.Source {
				fmt.Fprintf(tw, "\t\t%d\t|\t%s\n", warning.StartLine+offset, source)
			}
			fmt.Fprintln(tw)
		}
		fmt.Fprintln(tw)
	}

	return tw.Flush()
}
