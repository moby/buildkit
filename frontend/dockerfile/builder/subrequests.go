package builder

import (
	"context"
	"encoding/json"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/frontend/subrequests/outline"
	"github.com/moby/buildkit/frontend/subrequests/targets"
	"github.com/moby/buildkit/solver/errdefs"
)

func checkSubRequest(ctx context.Context, opts map[string]string) (*client.Result, bool, error) {
	req, ok := opts[keyRequestID]
	if !ok {
		return nil, false, nil
	}
	switch req {
	case subrequests.RequestSubrequestsDescribe:
		res, err := describe()
		return res, true, err
	case outline.RequestSubrequestsOutline, targets.RequestTargets: // handled later
		return nil, false, nil
	default:
		return nil, true, errdefs.NewUnsupportedSubrequestError(req)
	}
}

func describe() (*client.Result, error) {
	all := []subrequests.Request{
		subrequests.SubrequestsDescribeDefinition,
		outline.SubrequestsOutlineDefinition,
		targets.SubrequestsTargetsDefinition,
	}
	dt, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return nil, err
	}
	res := client.NewResult()
	res.Metadata = map[string][]byte{
		"result.json": dt,
	}
	return res, nil
}
