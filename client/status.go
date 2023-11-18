package client

import (
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/util"
	digest "github.com/opencontainers/go-digest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var emptyLogVertexSize int

func init() {
	emptyLogVertex := controlapi.VertexLog{}
	emptyLogVertexSize = proto.Size(&emptyLogVertex)
}

func NewSolveStatus(resp *controlapi.StatusResponse) *SolveStatus {
	s := &SolveStatus{}
	for _, v := range resp.Vertexes {
		s.Vertexes = append(s.Vertexes, &Vertex{
			Digest:        digest.Digest(v.Digest),
			Inputs:        util.FromStringSlice[digest.Digest](v.Inputs),
			Name:          v.Name,
			Started:       util.TimeOrNil(v.Started),
			Completed:     util.TimeOrNil(v.Completed),
			Error:         v.Error,
			Cached:        v.Cached,
			ProgressGroup: v.ProgressGroup,
		})
	}
	for _, v := range resp.Statuses {
		s.Statuses = append(s.Statuses, &VertexStatus{
			ID:        v.ID,
			Vertex:    digest.Digest(v.Vertex),
			Name:      v.Name,
			Total:     v.Total,
			Current:   v.Current,
			Timestamp: v.Timestamp.AsTime(),
			Started:   util.TimeOrNil(v.Started),
			Completed: util.TimeOrNil(v.Completed),
		})
	}
	for _, v := range resp.Logs {
		s.Logs = append(s.Logs, &VertexLog{
			Vertex:    digest.Digest(v.Vertex),
			Stream:    int(v.Stream),
			Data:      v.Msg,
			Timestamp: v.Timestamp.AsTime(),
		})
	}
	for _, v := range resp.Warnings {
		s.Warnings = append(s.Warnings, &VertexWarning{
			Vertex:     digest.Digest(v.Vertex),
			Level:      int(v.Level),
			Short:      v.Short,
			Detail:     v.Detail,
			URL:        v.Url,
			SourceInfo: v.Info,
			Range:      v.Ranges,
		})
	}
	return s
}

func (ss *SolveStatus) Marshal() (out []*controlapi.StatusResponse) {
	logSize := 0
	for {
		retry := false
		sr := controlapi.StatusResponse{}
		for _, v := range ss.Vertexes {
			sr.Vertexes = append(sr.Vertexes, &controlapi.Vertex{
				Digest:        v.Digest.String(),
				Inputs:        util.ToStringSlice(v.Inputs),
				Name:          v.Name,
				Started:       util.TimestampOrNil(v.Started),
				Completed:     util.TimestampOrNil(v.Completed),
				Error:         v.Error,
				Cached:        v.Cached,
				ProgressGroup: v.ProgressGroup,
			})
		}
		for _, v := range ss.Statuses {
			sr.Statuses = append(sr.Statuses, &controlapi.VertexStatus{
				ID:        v.ID,
				Vertex:    v.Vertex.String(),
				Name:      v.Name,
				Current:   v.Current,
				Total:     v.Total,
				Timestamp: timestamppb.New(v.Timestamp),
				Started:   util.TimestampOrNil(v.Started),
				Completed: util.TimestampOrNil(v.Completed),
			})
		}
		for i, v := range ss.Logs {
			sr.Logs = append(sr.Logs, &controlapi.VertexLog{
				Vertex:    v.Vertex.String(),
				Stream:    int64(v.Stream),
				Msg:       v.Data,
				Timestamp: timestamppb.New(v.Timestamp),
			})
			logSize += len(v.Data) + emptyLogVertexSize
			// avoid logs growing big and split apart if they do
			if logSize > 1024*1024 {
				ss.Vertexes = nil
				ss.Statuses = nil
				ss.Logs = ss.Logs[i+1:]
				retry = true
				break
			}
		}
		for _, v := range ss.Warnings {
			sr.Warnings = append(sr.Warnings, &controlapi.VertexWarning{
				Vertex: v.Vertex.String(),
				Level:  int64(v.Level),
				Short:  v.Short,
				Detail: v.Detail,
				Info:   v.SourceInfo,
				Ranges: v.Range,
				Url:    v.URL,
			})
		}
		out = append(out, &sr)
		if !retry {
			break
		}
	}
	return
}
