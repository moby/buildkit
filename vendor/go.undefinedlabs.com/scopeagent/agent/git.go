package agent

import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"go.undefinedlabs.com/scopeagent/env"
	"go.undefinedlabs.com/scopeagent/tags"
)

type (
	GitData struct {
		Repository string
		Commit     string
		SourceRoot string
		Branch     string
	}
	GitDiff struct {
		Type    string         `json:"type" msgpack:"type"`
		Version string         `json:"version" msgpack:"version"`
		Uuid    string         `json:"uuid" msgpack:"uuid"`
		Files   []DiffFileItem `json:"files" msgpack:"files"`
	}
	DiffFileItem struct {
		Path         string  `json:"path" msgpack:"path"`
		Added        int     `json:"added" msgpack:"added"`
		Removed      int     `json:"removed" msgpack:"removed"`
		Status       string  `json:"status" msgpack:"status"`
		PreviousPath *string `json:"previousPath" msgpack:"previousPath"`
	}
)

var (
	refRegex    = regexp.MustCompile(`(?m)ref:[ ]*(.*)$`)
	remoteRegex = regexp.MustCompile(`(?m)^\[remote[ ]*\"(.*)\"[ ]*\]$`)
	branchRegex = regexp.MustCompile(`(?m)^\[branch[ ]*\"(.*)\"[ ]*\]$`)
	urlRegex    = regexp.MustCompile(`(?m)url[ ]*=[ ]*(.*)$`)
	mergeRegex  = regexp.MustCompile(`(?m)merge[ ]*=[ ]*(.*)$`)
)

// Gets the current git data
func getGitData() *GitData {
	gitFolder, err := getGitFolder()
	if err != nil {
		return nil
	}

	var repository, commit, sourceRoot, branch string

	// Get source root
	sourceRoot = filepath.Dir(gitFolder)

	// Get commit hash
	var mergePath string
	if headFile, err := os.Open(filepath.Join(gitFolder, "HEAD")); err == nil {
		defer headFile.Close()
		if headBytes, err := ioutil.ReadAll(headFile); err == nil {
			head := string(headBytes)
			// HEAD data:  https://git-scm.com/book/en/v2/Git-Internals-Git-References
			refMatch := refRegex.FindStringSubmatch(head)
			if len(refMatch) == 2 {
				// Symbolic reference
				mergePath = strings.TrimSpace(refMatch[1])
				if refFile, err := os.Open(filepath.Join(gitFolder, mergePath)); err == nil {
					defer refFile.Close()
					if refBytes, err := ioutil.ReadAll(refFile); err == nil {
						commit = strings.TrimSpace(string(refBytes))
					}
				}
			} else {
				// Detached head (Plain hash)
				commit = strings.TrimSpace(head)
			}
		}
	}

	// Get repository and branch
	if configFile, err := os.Open(filepath.Join(gitFolder, "config")); err == nil {
		defer configFile.Close()
		reader := bufio.NewReader(configFile)
		scanner := bufio.NewScanner(reader)

		var tmpBranch string
		var intoRemoteBlock, intoBranchBlock bool
		for scanner.Scan() {
			line := scanner.Text()

			if repository == "" {
				if !intoRemoteBlock {
					remoteMatch := remoteRegex.FindStringSubmatch(line)
					if len(remoteMatch) == 2 {
						intoRemoteBlock = remoteMatch[1] == "origin"
						continue
					}
				} else {
					urlMatch := urlRegex.FindStringSubmatch(line)
					if len(urlMatch) == 2 {
						repository = strings.TrimSpace(urlMatch[1])
						intoRemoteBlock = false
						continue
					}
				}
			}

			if branch == "" {
				if !intoBranchBlock {
					branchMatch := branchRegex.FindStringSubmatch(line)
					if len(branchMatch) == 2 {
						tmpBranch = branchMatch[1]
						intoBranchBlock = true
						continue
					}
				} else {
					mergeMatch := mergeRegex.FindStringSubmatch(line)
					if len(mergeMatch) == 2 {
						mergeData := strings.TrimSpace(mergeMatch[1])
						intoBranchBlock = false
						if mergeData == mergePath {
							branch = tmpBranch
							continue
						}
					}
				}
			}
		}
	}

	return &GitData{
		Repository: repository,
		Commit:     commit,
		SourceRoot: sourceRoot,
		Branch:     branch,
	}
}

func getGitFolder() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", nil
	}
	for {
		rel, _ := filepath.Rel("/", dir)
		// Exit the loop once we reach the basePath.
		if rel == "." {
			return "", errors.New("git folder not found")
		}
		gitPath := fmt.Sprintf("%v/.git", dir)
		if pInfo, err := os.Stat(gitPath); err == nil && pInfo.IsDir() {
			return gitPath, nil
		}
		// Going up!
		dir += "/.."
	}
}

func getGitDiff() *GitDiff {
	var diff string
	if diffBytes, err := exec.Command("git", "diff", "--numstat").Output(); err == nil {
		diff = string(diffBytes)
	} else {
		return nil
	}

	reader := bufio.NewReader(strings.NewReader(diff))
	var files []DiffFileItem
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		diffItem := strings.Split(line, "\t")
		added, _ := strconv.Atoi(diffItem[0])
		removed, _ := strconv.Atoi(diffItem[1])
		path := strings.TrimSuffix(diffItem[2], "\n")

		files = append(files, DiffFileItem{
			Path:         path,
			Added:        added,
			Removed:      removed,
			Status:       "Modified",
			PreviousPath: nil,
		})
	}

	id, _ := uuid.NewRandom()
	gitDiff := GitDiff{
		Type:    "com.undefinedlabs.ugdsf",
		Version: "0.1.0",
		Uuid:    id.String(),
		Files:   files,
	}
	return &gitDiff
}

func getGitInfoFromGitFolder() map[string]interface{} {
	gitData := getGitData()

	if gitData == nil {
		return nil
	}

	gitInfo := map[string]interface{}{}

	if gitData.Repository != "" {
		gitInfo[tags.Repository] = gitData.Repository
	}
	if gitData.Commit != "" {
		gitInfo[tags.Commit] = gitData.Commit
	}
	if gitData.SourceRoot != "" {
		gitInfo[tags.SourceRoot] = gitData.SourceRoot
	}
	if gitData.Branch != "" {
		gitInfo[tags.Branch] = gitData.Branch
	}

	return gitInfo
}

func getGitInfoFromEnv() map[string]interface{} {
	gitInfo := map[string]interface{}{}

	if repository, set := env.ScopeRepository.Tuple(); set && repository != "" {
		gitInfo[tags.Repository] = repository
	}
	if commit, set := env.ScopeCommitSha.Tuple(); set && commit != "" {
		gitInfo[tags.Commit] = commit
	}
	if sourceRoot, set := env.ScopeSourceRoot.Tuple(); set && sourceRoot != "" {
		// We check if is a valid and existing folder
		if fInfo, err := os.Stat(sourceRoot); err == nil && fInfo.IsDir() {
			gitInfo[tags.SourceRoot] = sourceRoot
		}
	}
	if branch, set := env.ScopeBranch.Tuple(); set && branch != "" {
		gitInfo[tags.Branch] = branch
	}

	return gitInfo
}
