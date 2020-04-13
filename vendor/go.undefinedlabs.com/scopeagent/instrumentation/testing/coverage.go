package testing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	_ "unsafe"

	"github.com/google/uuid"
	"go.undefinedlabs.com/scopeagent/instrumentation"
)

type (
	coverage struct {
		Type    string         `json:"type" msgpack:"type"`
		Version string         `json:"version" msgpack:"version"`
		Uuid    string         `json:"uuid" msgpack:"uuid"`
		Files   []fileCoverage `json:"files" msgpack:"files"`
	}
	fileCoverage struct {
		Filename   string  `json:"filename" msgpack:"filename"`
		Boundaries [][]int `json:"boundaries" msgpack:"boundaries"`
	}
	pkg struct {
		ImportPath string
		Dir        string
		Error      *struct {
			Err string
		}
	}
	blockWithCount struct {
		block *testing.CoverBlock
		count int
	}
)

//go:linkname cover testing.cover
var (
	cover         testing.Cover
	counters      map[string][]uint32
	countersMutex sync.Mutex
	filePathData  map[string]string
	initOnce      sync.Once
)

// Initialize coverage
func initCoverage() {
	initOnce.Do(func() {
		var files []string
		for key := range cover.Blocks {
			files = append(files, key)
		}
		pkgData, err := findPkgs(files)
		if err != nil {
			pkgData = map[string]*pkg{}
			instrumentation.Logger().Printf("coverage error: %v", err)
		}
		filePathData = map[string]string{}
		for key := range cover.Blocks {
			filePath, err := findFile(pkgData, key)
			if err != nil {
				instrumentation.Logger().Printf("coverage error: %v", err)
			} else {
				filePathData[key] = filePath
			}
		}
		counters = map[string][]uint32{}
	})
}

// Clean the counters for a new coverage session
func startCoverage() {
	countersMutex.Lock()
	defer countersMutex.Unlock()
	if cover.Mode == "" {
		return
	}
	initCoverage()

	for name, counts := range cover.Counters {
		counters[name] = make([]uint32, len(counts))
		for i := range counts {
			counters[name][i] = atomic.SwapUint32(&counts[i], 0)
		}
	}
}

// Restore counters
func restoreCoverageCounters() {
	countersMutex.Lock()
	defer countersMutex.Unlock()
	if cover.Mode == "" {
		return
	}
	for name, counts := range cover.Counters {
		for i := range counts {
			atomic.StoreUint32(&counts[i], counters[name][i]+atomic.LoadUint32(&counts[i]))
		}
	}
}

// Get the counters values and extract the coverage info
func endCoverage() *coverage {
	countersMutex.Lock()
	defer countersMutex.Unlock()
	if cover.Mode == "" {
		return nil
	}

	var covSource = map[string][]*blockWithCount{}
	for name, counts := range cover.Counters {
		if file, ok := filePathData[name]; ok {
			blocks := cover.Blocks[name]
			for i := range counts {
				count := atomic.LoadUint32(&counts[i])
				atomic.StoreUint32(&counts[i], counters[name][i]+count)
				covSource[file] = append(covSource[file], &blockWithCount{
					block: &blocks[i],
					count: int(count),
				})
			}
			sort.SliceStable(covSource[file][:], func(i, j int) bool {
				if covSource[file][i].block.Line0 == covSource[file][j].block.Line0 {
					return covSource[file][i].block.Col0 < covSource[file][j].block.Col0
				}
				return covSource[file][i].block.Line0 < covSource[file][j].block.Line0
			})
		}
	}

	fileMap := map[string][][]int{}
	for file, blockCount := range covSource {
		blockStack := make([]*blockWithCount, 0)
		for _, curBlock := range blockCount {
			if curBlock.count > 0 {
				var prvBlock *testing.CoverBlock
				blockStackLen := len(blockStack)
				if blockStackLen > 0 {
					prvBlock = blockStack[blockStackLen-1].block
				}

				if prvBlock == nil {
					fileMap[file] = append(fileMap[file], []int{
						int(curBlock.block.Line0), int(curBlock.block.Col0), curBlock.count,
					})
					blockStack = append(blockStack, curBlock)
				} else if contains(prvBlock, curBlock.block) {
					pBoundCol := int(curBlock.block.Col0)
					cBoundCol := int(curBlock.block.Col0)
					if pBoundCol > 0 {
						pBoundCol--
					} else {
						cBoundCol++
					}
					fileMap[file] = append(fileMap[file], []int{
						int(curBlock.block.Line0), pBoundCol, -1,
					})
					fileMap[file] = append(fileMap[file], []int{
						int(curBlock.block.Line0), cBoundCol, curBlock.count,
					})
					blockStack = append(blockStack, curBlock)
				} else {
					pBoundCol := int(prvBlock.Col1)
					cBoundCol := int(curBlock.block.Col0)
					if prvBlock.Line1 == curBlock.block.Line0 {
						if pBoundCol > 0 {
							pBoundCol--
						} else {
							cBoundCol++
						}
					}
					fileMap[file] = append(fileMap[file], []int{
						int(prvBlock.Line1), pBoundCol, -1,
					})
					fileMap[file] = append(fileMap[file], []int{
						int(curBlock.block.Line0), cBoundCol, curBlock.count,
					})
					blockStack[blockStackLen-1] = curBlock
				}
			}
		}

		if len(blockStack) > 0 {
			var prvBlock *blockWithCount
			for i := len(blockStack) - 1; i >= 0; i-- {
				cBlock := blockStack[i]
				if prvBlock != nil {
					fileMap[file] = append(fileMap[file], []int{
						int(prvBlock.block.Line1), int(prvBlock.block.Col1) + 1, cBlock.count,
					})
				}
				fileMap[file] = append(fileMap[file], []int{
					int(cBlock.block.Line1), int(cBlock.block.Col1), -1,
				})
				prvBlock = cBlock
			}
		}
	}
	files := make([]fileCoverage, 0)
	for key, value := range fileMap {
		files = append(files, fileCoverage{
			Filename:   key,
			Boundaries: value,
		})
	}
	uuidValue, _ := uuid.NewRandom()
	coverageData := &coverage{
		Type:    "com.undefinedlabs.uccf",
		Version: "0.2.0",
		Uuid:    uuidValue.String(),
		Files:   files,
	}
	return coverageData
}

func contains(outer, inner *testing.CoverBlock) bool {
	if outer != nil && inner != nil {
		if outer.Line0 > inner.Line0 || (outer.Line0 == inner.Line0 && outer.Col0 > inner.Col0) {
			return false
		}
		if outer.Line1 < inner.Line1 || (outer.Line1 == inner.Line1 && outer.Col1 < inner.Col1) {
			return false
		}
		return true
	}
	return false
}

// The following functions are to find the absolute path from coverage data.
// There are extracted from the go cover cmd tool: https://github.com/golang/go/blob/master/src/cmd/cover/func.go

func findPkgs(fileNames []string) (map[string]*pkg, error) {
	// Run go list to find the location of every package we care about.
	pkgs := make(map[string]*pkg)
	var list []string
	for _, filename := range fileNames {
		if strings.HasPrefix(filename, ".") || filepath.IsAbs(filename) {
			// Relative or absolute path.
			continue
		}
		pkg := path.Dir(filename)
		if _, ok := pkgs[pkg]; !ok {
			pkgs[pkg] = nil
			list = append(list, pkg)
		}
	}

	if len(list) == 0 {
		return pkgs, nil
	}

	// Note: usually run as "go tool cover" in which case $GOROOT is set,
	// in which case runtime.GOROOT() does exactly what we want.
	goTool := filepath.Join(runtime.GOROOT(), "bin/go")
	cmd := exec.Command(goTool, append([]string{"list", "-e", "-json"}, list...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cannot run go list: %v\n%s", err, stderr.Bytes())
	}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var pkg pkg
		err := dec.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decoding go list json: %v", err)
		}
		pkgs[pkg.ImportPath] = &pkg
	}
	return pkgs, nil
}

// findFile finds the location of the named file in GOROOT, GOPATH etc.
func findFile(pkgs map[string]*pkg, file string) (string, error) {
	if strings.HasPrefix(file, ".") || filepath.IsAbs(file) {
		// Relative or absolute path.
		return file, nil
	}
	pkg := pkgs[path.Dir(file)]
	if pkg != nil {
		if pkg.Dir != "" {
			return filepath.Join(pkg.Dir, path.Base(file)), nil
		}
		if pkg.Error != nil {
			return "", errors.New(pkg.Error.Err)
		}
	}
	return "", fmt.Errorf("did not find package for %s in go list output", file)
}
