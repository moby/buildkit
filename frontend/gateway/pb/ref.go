package moby_buildkit_v1_frontend

func NewRef(id string) *Ref {
	var ref Ref
	if id != "" {
		ref.Ids = append(ref.Ids, id)
	}
	return &ref
}
