//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path"
	"regexp"
	"strings"
	"text/template"

	"github.com/pkg/errors"
)

type Rule struct {
	Name        string
	Description string
}

const tmplStr = `---
title: {{.Rule.Name}}
description: {{.Rule.Description}}
---

{{.Content}}
`

var destDir string

func main() {
	if len(os.Args) < 2 {
		panic("Please provide a destination directory")
	}
	destDir = os.Args[1]
	log.Printf("Destination directory: %s\n", destDir)
	if err := run(destDir); err != nil {
		panic(err)
	}
}

func run(destDir string) error {
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return err
	}
	rules, err := listRules()
	if err != nil {
		return err
	}
	tmpl, err := template.New("rule").Parse(tmplStr)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if ok, err := genRuleDoc(rule, tmpl); err != nil {
			return errors.Wrapf(err, "Error generating docs for %s", rule.Name)
		} else if ok {
			log.Printf("Docs generated for %s\n", rule.Name)
		}
	}
	return nil
}

func genRuleDoc(rule Rule, tmpl *template.Template) (bool, error) {
	mdfilename := fmt.Sprintf("docs/%s.md", rule.Name)
	content, err := os.ReadFile(mdfilename)
	if err != nil {
		return false, err
	}
	outputfile, err := os.Create(path.Join(destDir, fmt.Sprintf("%s.md", camelToKebab(rule.Name))))
	if err != nil {
		return false, err
	}
	defer outputfile.Close()
	if err = tmpl.Execute(outputfile, struct {
		Rule    Rule
		Content string
	}{
		Rule:    rule,
		Content: string(content),
	}); err != nil {
		return false, err
	}
	return true, nil
}

func listRules() ([]Rule, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "ruleset.go", nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var rules []Rule
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.GenDecl:
			for _, spec := range x.Specs {
				if vSpec, ok := spec.(*ast.ValueSpec); ok {
					rule := Rule{}
					if cl, ok := vSpec.Values[0].(*ast.CompositeLit); ok {
						for _, elt := range cl.Elts {
							if kv, ok := elt.(*ast.KeyValueExpr); ok {
								switch kv.Key.(*ast.Ident).Name {
								case "Name":
									if basicLit, ok := kv.Value.(*ast.BasicLit); ok {
										rule.Name = strings.Trim(basicLit.Value, `"`)
									}
								case "Description":
									if basicLit, ok := kv.Value.(*ast.BasicLit); ok {
										rule.Description = strings.Trim(basicLit.Value, `"`)
									}
								}
							}
						}
					}
					rules = append(rules, rule)
				}
			}
		}
		return true
	})
	return rules, nil
}

func camelToKebab(s string) string {
	var re = regexp.MustCompile(`([a-z])([A-Z])`)
	return strings.ToLower(re.ReplaceAllString(s, `${1}-${2}`))
}
