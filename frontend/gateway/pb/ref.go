package moby_buildkit_v1_frontend

import "github.com/moby/buildkit/solver/pb"

func NewRef(id string, def *pb.Definition) *Ref {
	var ref Ref
	if id != "" {
		ref.Ids = append(ref.Ids, id)
	}
	if def != nil {
		ref.Defs = append(ref.Defs, def)
	}
	return &ref
}
