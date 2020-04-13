package ast

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	methodCodes map[string]map[string]*MethodCodeBoundaries
	mutex       sync.Mutex
)

type MethodCodeBoundaries struct {
	Package string
	Name    string
	File    string
	Start   CodePos
	End     CodePos
}
type CodePos struct {
	Line   int
	Column int
}

// Gets the function source code boundaries from the caller method
func GetFuncSourceFromCaller(skip int) (*MethodCodeBoundaries, error) {
	pc, _, _, _ := runtime.Caller(skip + 1)
	return GetFuncSource(pc)
}

// Gets the function source code boundaries from a method
func GetFuncSourceForName(pc uintptr, name string) (*MethodCodeBoundaries, error) {
	mFunc := runtime.FuncForPC(pc)
	mFile, _ := mFunc.FileLine(pc)
	mFile = filepath.Clean(mFile)
	fileCode, err := getCodesForFile(mFile)
	if err != nil {
		return nil, err
	}
	return fileCode[name], nil
}

// Gets the function source code boundaries from a method
func GetFuncSource(pc uintptr) (*MethodCodeBoundaries, error) {
	mFunc := runtime.FuncForPC(pc)
	mFile, _ := mFunc.FileLine(pc)
	mFile = filepath.Clean(mFile)
	fileCode, err := getCodesForFile(mFile)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(mFunc.Name(), ".")
	funcName := parts[len(parts)-1]
	return fileCode[funcName], nil
}

func getCodesForFile(file string) (map[string]*MethodCodeBoundaries, error) {
	mutex.Lock()
	defer mutex.Unlock()
	if methodCodes == nil {
		methodCodes = map[string]map[string]*MethodCodeBoundaries{}
	}
	if methodCodes[file] == nil {
		methodCodes[file] = map[string]*MethodCodeBoundaries{}

		fSet := token.NewFileSet()
		f, err := parser.ParseFile(fSet, file, nil, 0)
		if err != nil {
			return nil, err
		}

		packageName := f.Name.String()
		for _, decl := range f.Decls {
			if fDecl, ok := decl.(*ast.FuncDecl); ok {
				bPos := fDecl.Pos()
				if fDecl.Body != nil {
					bEnd := fDecl.Body.End()
					if bPos.IsValid() && bEnd.IsValid() {
						pos := fSet.PositionFor(bPos, true)
						end := fSet.PositionFor(bEnd, true)
						methodCode := MethodCodeBoundaries{
							Package: packageName,
							Name:    fDecl.Name.String(),
							File:    file,
							Start: CodePos{
								Line:   pos.Line,
								Column: pos.Column,
							},
							End: CodePos{
								Line:   end.Line,
								Column: end.Column,
							},
						}
						methodCodes[file][methodCode.Name] = &methodCode
					}
				}
			}
		}
	}
	return methodCodes[file], nil
}
