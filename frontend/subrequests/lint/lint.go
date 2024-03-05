package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
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
	Short    string        `json:"short"`
	Detail   [][]byte      `json:"detail"`
	Location *parser.Range `json:"location"`
	Filename string        `json:"filename"`
	Source   []string      `json:"source"`
}

type WarningList struct {
	Warnings []Warning `json:"warnings"`
}

func (warns WarningList) ToResult() (*client.Result, error) {
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
	var warnings WarningList

	if err := json.Unmarshal(dt, &warnings); err != nil {
		return err
	}

	// Group warnings by short
	lintWarnings := make(map[string][]Warning)
	lintWarningShorts := []string{}
	for _, warning := range warnings.Warnings {
		if _, ok := lintWarnings[warning.Short]; !ok {
			lintWarningShorts = append(lintWarningShorts, warning.Short)
			lintWarnings[warning.Short] = []Warning{}
		}
		lintWarnings[warning.Short] = append(lintWarnings[warning.Short], warning)
	}
	sort.Strings(lintWarningShorts)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Lint Warnings\n")
	for _, short := range lintWarningShorts {
		fmt.Fprintf(tw, "\t%s\n", short)
		for _, warning := range lintWarnings[short] {
			fmt.Fprintf(tw, "\t\t%s:%d\n", warning.Filename, warning.Location.Start.Line)
			for _, line := range warning.Source {
				fmt.Fprintf(tw, "\t\t%s\n", line)
			}
			fmt.Fprintf(tw, "\n")
		}
	}

	return tw.Flush()
}
