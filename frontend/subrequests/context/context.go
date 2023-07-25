package context

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
)

const RequestSubrequestsContext = "frontend.context"

var SubrequestsContextDefinition = subrequests.Request{
	Name:        RequestSubrequestsContext,
	Version:     "1.0.0",
	Type:        subrequests.TypeRPC,
	Description: "",
	Opts: []subrequests.Named{
		{
			Name:        "target",
			Description: "Target build stage",
		},
	},
	Metadata: []subrequests.Named{
		{Name: "result.json"},
		{Name: "result.txt"},
	},
}

type Context struct {
	Files []File `json:"files,omitempty"`
}

type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (c Context) ToResult() (*client.Result, error) {
	res := client.NewResult()
	dt, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	res.AddMeta("result.json", dt)

	b := bytes.NewBuffer(nil)
	if err := PrintContext(dt, b); err != nil {
		return nil, err
	}
	res.AddMeta("result.txt", b.Bytes())

	res.AddMeta("version", []byte(SubrequestsContextDefinition.Version))
	return res, nil
}

func PrintContext(dt []byte, w io.Writer) error {
	var o Context

	if err := json.Unmarshal(dt, &o); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintf(tw, "NAME\tSIZE\n")

	for _, f := range o.Files {
		fmt.Fprintf(tw, "%s\t%d\n", f.Name, f.Size)
	}
	tw.Flush()
	fmt.Fprintln(tw)

	return nil
}
