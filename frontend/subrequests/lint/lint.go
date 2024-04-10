package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/solver/pb"
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
	Filename   string         `json:"fileName"`
	Language   string         `json:"language"`
	Definition *pb.Definition `json:"definition"`
	Data       []byte         `json:"data"`
}

type Warning struct {
	RuleName    string      `json:"ruleName"`
	Description string      `json:"description,omitempty"`
	URL         string      `json:"url,omitempty"`
	Detail      string      `json:"detail,omitempty"`
	Location    pb.Location `json:"location,omitempty"`
}

type LintResults struct {
	Warnings []Warning `json:"warnings"`
	Sources  []Source  `json:"sources"`
}

func (results *LintResults) AddSource(sourceMap *llb.SourceMap) int {
	newSource := Source{
		Filename:   sourceMap.Filename,
		Language:   sourceMap.Language,
		Definition: sourceMap.Definition.ToPB(),
		Data:       sourceMap.Data,
	}
	for i, source := range results.Sources {
		if sourceEqual(source, newSource) {
			return i
		}
	}
	results.Sources = append(results.Sources, newSource)
	return len(results.Sources) - 1
}

func (results *LintResults) AddWarning(rulename, description, url, fmtmsg string, sourceIndex int, location []parser.Range) {
	sourceLocation := []*pb.Range{}
	for _, loc := range location {
		sourceLocation = append(sourceLocation, &pb.Range{
			Start: pb.Position{
				Line:      int32(loc.Start.Line),
				Character: int32(loc.Start.Character),
			},
			End: pb.Position{
				Line:      int32(loc.End.Line),
				Character: int32(loc.End.Character),
			},
		})
	}
	pbLocation := pb.Location{
		SourceIndex: int32(sourceIndex),
		Ranges:      sourceLocation,
	}
	results.Warnings = append(results.Warnings, Warning{
		RuleName:    rulename,
		Description: description,
		URL:         url,
		Detail:      fmtmsg,
		Location:    pbLocation,
	})
}

func sourceEqual(a, b Source) bool {
	if a.Filename != b.Filename || a.Language != b.Language {
		return false
	}
	return bytes.Equal(a.Data, b.Data)
}

func (results *LintResults) ToResult() (*client.Result, error) {
	res := client.NewResult()
	dt, err := json.MarshalIndent(results, "", "  ")
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
			source := warnings.Sources[warning.Location.SourceIndex]
			sourceData := bytes.Split(source.Data, []byte("\n"))
			firstRange := warning.Location.Ranges[0]
			if firstRange.Start.Line != firstRange.End.Line {
				fmt.Fprintf(tw, "\t%s:%d-%d\n", source.Filename, firstRange.Start.Line, firstRange.End.Line)
			} else {
				fmt.Fprintf(tw, "\t%s:%d\n", source.Filename, firstRange.Start.Line)
			}
			fmt.Fprintf(tw, "\t%s\n", warning.Detail)
			for _, r := range warning.Location.Ranges {
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
