package outline

import (
	"encoding/json"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/solver/pb"
)

const RequestSubrequestsOutline = "frontend.outline"

var SubrequestsOutlineDefinition = subrequests.Request{
	Name:        RequestSubrequestsOutline,
	Version:     "1.0.0",
	Type:        subrequests.TypeRPC,
	Description: "List all parameters current build takes",
	Opts: []subrequests.Named{
		{
			Name:        "target",
			Description: "Target build stage",
		},
	},
	Metadata: []subrequests.Named{
		{
			Name: "result.json",
		},
	},
}

type Outline struct {
	Args    []Arg        `json:"args,omitempty"`
	Secret  []Secret     `json:"secret,omitempty"`
	SSH     []SSH        `json:"ssh,omitempty"`
	Cache   []CacheMount `json:"cache,omitempty"`
	Sources [][]byte     `json:"sources,omitempty"`
}

func (o Outline) ToResult() (*client.Result, error) {
	res := client.NewResult()
	dt, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	res.AddMeta("result.json", dt)
	return res, nil
}

type Arg struct {
	Name        string
	Description string
	Value       string
	Location    *pb.Location
}

type Secret struct {
	Name     string
	Required bool
}

type SSH struct {
	Name     string
	Required bool
}

type CacheMount struct {
	ID string
}
