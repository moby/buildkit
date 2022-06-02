package dockerfile2llb

import (
	"bufio"
	"io"
	"regexp"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

const keySyntax = "syntax"

var reShebang = regexp.MustCompile(`^#!.+$`)
var reDirective = regexp.MustCompile(`^(?:#|\/{2})\s*([a-zA-Z][a-zA-Z0-9]*)\s*=\s*(.+?)\s*$`)

type Directive struct {
	Name     string
	Value    string
	Location []parser.Range
}

func DetectSyntax(r io.Reader) (string, string, []parser.Range, bool) {
	directives := ParseDirectives(r)
	if len(directives) == 0 {
		return "", "", nil, false
	}
	v, ok := directives[keySyntax]
	if !ok {
		return "", "", nil, false
	}
	p := strings.SplitN(v.Value, " ", 2)
	return p[0], v.Value, v.Location, true
}

func ParseDirectives(r io.Reader) map[string]Directive {
	m := map[string]Directive{}
	s := bufio.NewScanner(r)
	var l int
	for s.Scan() {
		l++
		if reShebang.MatchString(s.Text()) {
			// If a line contains a shebang, skip the
			// line and continue parsing directives.
			continue
		}
		match := reDirective.FindStringSubmatch(s.Text())
		if len(match) == 0 {
			// If a line is encountered which does not
			// contain a directive, stop parsing.
			return m
		}
		m[strings.ToLower(match[1])] = Directive{
			Name:  match[1],
			Value: match[2],
			Location: []parser.Range{{
				Start: parser.Position{Line: l},
				End:   parser.Position{Line: l},
			}},
		}
	}
	return m
}
