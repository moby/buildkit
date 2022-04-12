package build

func ParseOpt(opts []string) (map[string]string, error) {
	return attrMap(opts)
}
