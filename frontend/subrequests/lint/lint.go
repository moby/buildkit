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

type Source struct {
	Filename string `json:"filename"`
	Language string `json:"language"`
	Data     []byte `json:"data"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type Warning struct {
	RuleName    string  `json:"rule_name"`
	Detail      string  `json:"detail"`
	SourceIndex int     `json:"source_index"`
	Location    []Range `json:"location"`
}

type LintResults struct {
	Warnings []Warning `json:"warnings"`
	Sources  []Source  `json:"sources"`
}

func (results *LintResults) AddSource(filename string, language string, data []byte) int {
	sourceE := Source{
		Filename: filename,
		Language: language,
		Data:     data,
	}
	for i, source := range results.Sources {
		if sourceEqual(source, sourceE) {
			return i
		}
	}
	results.Sources = append(results.Sources, Source{
		Filename: filename,
		Language: language,
		Data:     data,
	})
	return len(results.Sources) - 1
}

func (results *LintResults) AddWarning(ruleName, detail string, sourceIndex int, location []Range) {
	results.Warnings = append(results.Warnings, Warning{
		RuleName:    ruleName,
		Detail:      detail,
		SourceIndex: sourceIndex,
		Location:    location,
	})
}

func sourceEqual(a, b Source) bool {
	if a.Filename != b.Filename || a.Language != b.Language {
		return false
	}
	return bytes.Equal(a.Data, b.Data)
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

	// Here, we're grouping the warnings by rule name
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

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, rule := range lintWarningRules {
		fmt.Fprintf(tw, "Lint Rule %s\n", rule)
		for _, warning := range lintWarnings[rule] {
			source := warnings.Sources[warning.SourceIndex]
			sourceData := bytes.Split(source.Data, []byte("\n"))
			firstRange := warning.Location[0]
			if firstRange.Start.Line != firstRange.End.Line {
				fmt.Fprintf(tw, "\t%s:%d-%d\n", source.Filename, firstRange.Start.Line, firstRange.End.Line)
			} else {
				fmt.Fprintf(tw, "\t%s:%d\n", source.Filename, firstRange.Start.Line)
			}
			fmt.Fprintf(tw, "\t%s\n", warning.Detail)
			for _, r := range warning.Location {
				for i := r.Start.Line; i <= r.End.Line; i++ {
					fmt.Fprintf(tw, "\t%d\t|\t%s\n", i, sourceData[i-1])
				}
			}
			fmt.Fprintln(tw)
		}
		fmt.Fprintln(tw)
	}

	return tw.Flush()
}
