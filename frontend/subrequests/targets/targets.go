package targets

import (
	"encoding/json"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/solver/pb"
)

const RequestTargets = "frontend.targets"

var SubrequestsTargetsDefinition = subrequests.Request{
	Name:        RequestTargets,
	Version:     "1.0.0",
	Type:        subrequests.TypeRPC,
	Description: "List all targets current build takes",
	Opts:        []subrequests.Named{},
	Metadata: []subrequests.Named{
		{
			Name: "result.json",
		},
	},
}

type List struct {
	Targets []Target
	Sources [][]byte
}

func (l List) ToResult() (*client.Result, error) {
	res := client.NewResult()
	dt, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	res.AddMeta("result.json", dt)
	return res, nil
}

type Target struct {
	Name        string `json:"name,omitempty"`
	Default     bool   `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
	Location    *pb.Location
}
