package fsutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/pkg/fileutils"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil/types"
)

type WalkOpt struct {
	IncludePatterns []string
	ExcludePatterns []string
	// FollowPaths contains symlinks that are resolved into include patterns
	// before performing the fs walk
	FollowPaths []string
	Map         FilterFunc
	VerboseProgressCB VerboseProgressCB // earthly-specific
}

func Walk(ctx context.Context, p string, opt *WalkOpt, fn filepath.WalkFunc) error {
	root, err := filepath.EvalSymlinks(p)
	if err != nil {
		return errors.WithStack(&os.PathError{Op: "resolve", Path: root, Err: err})
	}
	fi, err := os.Stat(root)
	if err != nil {
		return errors.WithStack(err)
	}
	if !fi.IsDir() {
		return errors.WithStack(&os.PathError{Op: "walk", Path: root, Err: syscall.ENOTDIR})
	}

	var (
		includePatterns []string
		includeMatcher  *fileutils.PatternMatcher
		excludeMatcher  *fileutils.PatternMatcher
	)

	if opt != nil && opt.IncludePatterns != nil {
		includePatterns = make([]string, len(opt.IncludePatterns))
		copy(includePatterns, opt.IncludePatterns)
	}
	if opt != nil && opt.FollowPaths != nil {
		targets, err := FollowLinks(p, opt.FollowPaths)
		if err != nil {
			return err
		}
		if targets != nil {
			includePatterns = append(includePatterns, targets...)
			includePatterns = dedupePaths(includePatterns)
		}
	}
	if len(includePatterns) != 0 {
		includeMatcher, err = fileutils.NewPatternMatcher(includePatterns)
		if err != nil {
			return errors.Wrapf(err, "invalid includepatterns: %s", opt.IncludePatterns)
		}
	}

	if opt != nil && opt.ExcludePatterns != nil {
		excludeMatcher, err = fileutils.NewPatternMatcher(opt.ExcludePatterns)
		if err != nil {
			return errors.Wrapf(err, "invalid excludepatterns: %s", opt.ExcludePatterns)
		}
	}

	type visitedDir struct {
		fi             os.FileInfo
		path           string
		origpath       string
		pathWithSep    string
		matchedInclude bool
		matchedExclude bool
		calledFn       bool
	}

	// used only for include/exclude handling
	var parentDirs []visitedDir

	seenFiles := make(map[uint64]string)
	return filepath.Walk(root, func(path string, fi os.FileInfo, err error) (retErr error) {
		defer func() {
			if retErr != nil && isNotExist(retErr) {
				retErr = filepath.SkipDir
			}
		}()
		if err != nil {
			return err
		}

		origpath := path
		path, err = filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Skip root
		if path == "." {
			return nil
		}

		var dir visitedDir

		if includeMatcher != nil || excludeMatcher != nil {
			for len(parentDirs) != 0 {
				lastParentDir := parentDirs[len(parentDirs)-1].pathWithSep
				if strings.HasPrefix(path, lastParentDir) {
					break
				}
				parentDirs = parentDirs[:len(parentDirs)-1]
			}

			if fi.IsDir() {
				dir = visitedDir{
					fi:          fi,
					path:        path,
					origpath:    origpath,
					pathWithSep: path + string(filepath.Separator),
				}
			}
		}

		skip := false

		if includeMatcher != nil {
			var parentMatchedInclude *bool
			if len(parentDirs) != 0 {
				parentMatchedInclude = &parentDirs[len(parentDirs)-1].matchedInclude
			}
			m, err := matchesPatterns(includeMatcher, path, parentMatchedInclude)
			if err != nil {
				return errors.Wrap(err, "failed to match includepatterns")
			}

			if fi.IsDir() {
				dir.matchedInclude = m
			}

			if !m {
				skip = true
			}
		}

		if excludeMatcher != nil {
			var parentMatchedExclude *bool
			if len(parentDirs) != 0 {
				parentMatchedExclude = &parentDirs[len(parentDirs)-1].matchedExclude
			}
			m, err := matchesPatterns(excludeMatcher, path, parentMatchedExclude)
			if err != nil {
				return errors.Wrap(err, "failed to match excludepatterns")
			}

			if fi.IsDir() {
				dir.matchedExclude = m
			}

			if m {
				if opt.VerboseProgressCB != nil { // earthly-specific
					opt.VerboseProgressCB(path, StatusSkipped, 0)
				}
				if fi.IsDir() && !excludeMatcher.Exclusions() {
					return filepath.SkipDir
				}
				skip = true
			}
		}

		if includeMatcher != nil || excludeMatcher != nil {
			defer func() {
				if fi.IsDir() {
					parentDirs = append(parentDirs, dir)
				}
			}()
		}

		if skip {
			return nil
		}

		dir.calledFn = true

		stat, err := mkstat(origpath, path, fi, seenFiles)
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if opt != nil && opt.Map != nil {
				if allowed := opt.Map(stat.Path, stat); !allowed {
					return nil
				}
			}
			for i, parentDir := range parentDirs {
				if parentDir.calledFn {
					continue
				}
				parentStat, err := mkstat(parentDir.origpath, parentDir.path, parentDir.fi, seenFiles)
				if err != nil {
					return err
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				if opt != nil && opt.Map != nil {
					if allowed := opt.Map(parentStat.Path, parentStat); !allowed {
						continue
					}
				}

				if err := fn(parentStat.Path, &StatInfo{parentStat}, nil); err != nil {
					return err
				}
				parentDirs[i].calledFn = true
			}
			if err := fn(stat.Path, &StatInfo{stat}, nil); err != nil {
				return err
			}
		}
		return nil
	})
}

func matchesPatterns(pm *fileutils.PatternMatcher, path string, parentMatched *bool) (bool, error) {
	var (
		m   bool
		err error
	)
	if parentMatched != nil {
		m, err = pm.MatchesUsingParentResult(path, *parentMatched)
	} else {
		m, err = pm.MatchesOrParentMatches(path)
	}
	if err != nil {
		return false, err
	}

	return m, nil
}

type StatInfo struct {
	*types.Stat
}

func (s *StatInfo) Name() string {
	return filepath.Base(s.Stat.Path)
}
func (s *StatInfo) Size() int64 {
	return s.Stat.Size_
}
func (s *StatInfo) Mode() os.FileMode {
	return os.FileMode(s.Stat.Mode)
}
func (s *StatInfo) ModTime() time.Time {
	return time.Unix(s.Stat.ModTime/1e9, s.Stat.ModTime%1e9)
}
func (s *StatInfo) IsDir() bool {
	return s.Mode().IsDir()
}
func (s *StatInfo) Sys() interface{} {
	return s.Stat
}

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
