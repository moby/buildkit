package build

// ParseLocal parses --local
func ParseLocal(locals []string) (map[string]string, error) {
	return attrMap(locals)
}
