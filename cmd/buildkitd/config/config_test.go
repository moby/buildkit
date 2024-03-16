package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
)

func TestGenerateDocs(t *testing.T) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "config.go", nil, parser.ParseComments)
	if err != nil {
		fmt.Println("Error parsing code:", err)
		return
	}

	configType := reflect.TypeOf(Config{})
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		fieldName := field.Name
		tagToml := field.Tag.Get("toml")

		comment := findComment(node, fieldName)
		if comment != nil {
			fmt.Printf("#%s\n", comment.Text[2:])
			fmt.Printf("[%v]\n", tagToml)
			fmt.Printf("\n")
		}
	}
}

func findComment(node *ast.File, fieldName string) *ast.Comment {
	for _, decl := range node.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "Config" {
				continue
			}
			if structType, ok := typeSpec.Type.(*ast.StructType); ok {
				for _, field := range structType.Fields.List {
					for _, name := range field.Names {
						if name.Name == fieldName {
							if len(field.Doc.List) > 0 {
								return field.Doc.List[0]
							}
						}
					}
				}
			}
		}
	}

	return nil
}
