package dockerui

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/frontend/subrequests/lint"
	"github.com/moby/buildkit/frontend/subrequests/outline"
	"github.com/moby/buildkit/frontend/subrequests/targets"
	"github.com/moby/buildkit/solver/errdefs"
)

const (
	keyRequestID = "requestid"
)

type PlatformCtxKey struct{}

type RequestHandler struct {
	Outline     func(context.Context) (*outline.Outline, error)
	ListTargets func(context.Context) (*targets.List, error)
	Lint        func(context.Context) (*lint.LintResults, error)
	AllowOther  bool
}

func (bc *Client) HandleSubrequest(ctx context.Context, h RequestHandler) (*client.Result, bool, error) {
	req, ok := bc.bopts.Opts[keyRequestID]
	if !ok {
		return nil, false, nil
	}
	switch req {
	case subrequests.RequestSubrequestsDescribe:
		res, err := describe(h)
		return res, true, err
	case outline.SubrequestsOutlineDefinition.Name:
		if f := h.Outline; f != nil {
			o, err := f(ctx)
			if err != nil {
				return nil, false, err
			}
			if o == nil {
				return nil, true, nil
			}
			res, err := o.ToResult()
			return res, true, err
		}
	case targets.SubrequestsTargetsDefinition.Name:
		if f := h.ListTargets; f != nil {
			targets, err := f(ctx)
			if err != nil {
				return nil, false, err
			}
			if targets == nil {
				return nil, true, nil
			}
			res, err := targets.ToResult()
			return res, true, err
		}
	case lint.SubrequestLintDefinition.Name:
		if f := h.Lint; f != nil {
			type warningKey struct {
				detail    string
				startLine int32
			}
			lintResults := lint.LintResults{}
			uniqueWarnings := map[warningKey]lint.Warning{}
			if len(bc.Config.TargetPlatforms) == 0 {
				if len(bc.Config.BuildPlatforms) != 0 {
					bc.Config.TargetPlatforms = append(bc.Config.TargetPlatforms, bc.Config.BuildPlatforms[0])
				}
			}
			for _, tp := range bc.Config.TargetPlatforms {
				ctx := context.WithValue(ctx, PlatformCtxKey{}, &tp)
				results, err := f(ctx)
				if err != nil {
					return nil, false, err
				}
				sourceStart := len(lintResults.Sources)
				lintResults.Sources = append(lintResults.Sources, results.Sources...)
				for _, warning := range results.Warnings {
					var startLine int32
					if len(warning.Location.Ranges) > 0 || warning.Location.Ranges[0] != nil {
						startLine = warning.Location.Ranges[0].Start.Line
					}
					key := warningKey{warning.Detail, startLine}
					if _, ok := uniqueWarnings[key]; !ok {
						uniqueWarnings[key] = warning
						// Update the location to be relative to the combined source infos
						warning.Location.SourceIndex += int32(sourceStart)
						lintResults.Warnings = append(lintResults.Warnings, warning)
					}
				}
				if results.Error != nil {
					lintResults.Error = results.Error
					break
				}
			}
			res, err := lintResults.ToResult(nil)
			return res, true, err
		}
	}
	if h.AllowOther {
		return nil, false, nil
	}
	return nil, false, errdefs.NewUnsupportedSubrequestError(req)
}

func describe(h RequestHandler) (*client.Result, error) {
	all := []subrequests.Request{}
	if h.Outline != nil {
		all = append(all, outline.SubrequestsOutlineDefinition)
	}
	if h.ListTargets != nil {
		all = append(all, targets.SubrequestsTargetsDefinition)
	}
	all = append(all, subrequests.SubrequestsDescribeDefinition)
	dt, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return nil, err
	}

	b := bytes.NewBuffer(nil)
	if err := subrequests.PrintDescribe(dt, b); err != nil {
		return nil, err
	}

	res := client.NewResult()
	res.Metadata = map[string][]byte{
		"result.json": dt,
		"result.txt":  b.Bytes(),
		"version":     []byte(subrequests.SubrequestsDescribeDefinition.Version),
	}
	return res, nil
}
