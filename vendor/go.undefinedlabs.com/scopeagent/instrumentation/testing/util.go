package testing

import (
	"fmt"
	"path"
	"runtime"
	"strings"

	"go.undefinedlabs.com/scopeagent/ast"
	"go.undefinedlabs.com/scopeagent/instrumentation"
)

func getPackageAndName(pc uintptr) (string, string) {
	return splitPackageAndName(runtime.FuncForPC(pc).Name())
}

func splitPackageAndName(funcFullName string) (string, string) {
	lastSlash := strings.LastIndexByte(funcFullName, '/')
	if lastSlash < 0 {
		lastSlash = 0
	}
	firstDot := strings.IndexByte(funcFullName[lastSlash:], '.') + lastSlash
	packName := funcFullName[:firstDot]
	// If the package has the format: _/{path...}
	// We convert the path from absolute to relative to the source root
	sourceRoot := instrumentation.GetSourceRoot()
	if len(packName) > 0 && packName[0] == '_' && strings.Index(packName, sourceRoot) != -1 {
		packName = strings.Replace(packName, path.Dir(sourceRoot)+"/", "", -1)[1:]
	}
	funcName := funcFullName[firstDot+1:]
	return packName, funcName
}

func getPackageAndNameAndBoundaries(pc uintptr) (string, string, string) {
	pName, fName := getPackageAndName(pc)
	dotIndex := strings.IndexByte(fName, '.')
	if dotIndex != -1 {
		fName = fName[:dotIndex]
	}

	fBoundaries := ""
	sourceBounds, err := ast.GetFuncSourceForName(pc, fName)
	if err != nil {
		instrumentation.Logger().Printf("error calculating the source boundaries for '%s': %v", fName, err)
	}
	if sourceBounds != nil {
		fBoundaries = fmt.Sprintf("%s:%d:%d", sourceBounds.File, sourceBounds.Start.Line, sourceBounds.End.Line)
	}
	return pName, fName, fBoundaries
}
