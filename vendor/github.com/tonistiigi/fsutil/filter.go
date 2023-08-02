package fsutil

import (
	"context"
	"io"
	gofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/moby/patternmatcher"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil/types"
)

type FilterOpt struct {
	IncludePatterns []string
	ExcludePatterns []string
	// FollowPaths contains symlinks that are resolved into include patterns
	// before performing the fs walk
	FollowPaths []string
	Map         MapFunc
}

type MapFunc func(string, *types.Stat) MapResult

// The result of the walk function controls
// both how WalkDir continues and whether the path is kept.
type MapResult int

const (
	// Keep the current path and continue.
	MapResultKeep MapResult = iota

	// Exclude the current path and continue.
	MapResultExclude

	// Exclude the current path, and skip the rest of the dir.
	// If path is a dir, skip the current directory.
	// If path is a file, skip the rest of the parent directory.
	// (This matches the semantics of fs.SkipDir.)
	MapResultSkipDir
)

type filterFS struct {
	fs  FS
	opt *FilterOpt
}

func NewFilterFS(fs FS, opt *FilterOpt) FS {
	if opt == nil {
		return fs
	}

	if fs, ok := fs.(*filterFS); ok {
		includes := append([]string{}, fs.opt.IncludePatterns...)
		includes = append(includes, opt.IncludePatterns...)
		excludes := append([]string{}, fs.opt.ExcludePatterns...)
		excludes = append(excludes, opt.ExcludePatterns...)
		follows := append([]string{}, fs.opt.FollowPaths...)
		follows = append(follows, opt.FollowPaths...)
		var mapFn MapFunc
		if fs.opt.Map != nil && opt.Map != nil {
			mapFn = func(path string, stat *types.Stat) MapResult {
				result := fs.opt.Map(path, stat)
				if result != MapResultKeep {
					return result
				}
				return opt.Map(path, stat)
			}
		} else if fs.opt.Map != nil {
			mapFn = fs.opt.Map
		} else if opt.Map != nil {
			mapFn = opt.Map
		}
		return &filterFS{fs, &FilterOpt{
			IncludePatterns: includes,
			ExcludePatterns: excludes,
			FollowPaths:     follows,
			Map:             mapFn,
		}}
	}

	return &filterFS{fs, opt}
}

func (fs *filterFS) Open(p string) (io.ReadCloser, error) {
	return fs.fs.Open(p)
}

func (fs *filterFS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	var (
		includePatterns []string
		includeMatcher  *patternmatcher.PatternMatcher
		excludeMatcher  *patternmatcher.PatternMatcher
		err             error
	)

	if fs.opt != nil && fs.opt.IncludePatterns != nil {
		includePatterns = make([]string, len(fs.opt.IncludePatterns))
		copy(includePatterns, fs.opt.IncludePatterns)
	}
	if fs.opt != nil && fs.opt.FollowPaths != nil {
		targets, err := FollowLinks(fs.fs, fs.opt.FollowPaths)
		if err != nil {
			return err
		}
		if targets != nil {
			includePatterns = append(includePatterns, targets...)
			includePatterns = dedupePaths(includePatterns)
		}
	}

	patternChars := "*[]?^"
	if os.PathSeparator != '\\' {
		patternChars += `\`
	}

	onlyPrefixIncludes := true
	if len(includePatterns) != 0 {
		includeMatcher, err = patternmatcher.New(includePatterns)
		if err != nil {
			return errors.Wrapf(err, "invalid includepatterns: %s", fs.opt.IncludePatterns)
		}

		for _, p := range includeMatcher.Patterns() {
			if !p.Exclusion() && strings.ContainsAny(patternWithoutTrailingGlob(p), patternChars) {
				onlyPrefixIncludes = false
				break
			}
		}

	}

	onlyPrefixExcludeExceptions := true
	if fs.opt != nil && fs.opt.ExcludePatterns != nil {
		excludeMatcher, err = patternmatcher.New(fs.opt.ExcludePatterns)
		if err != nil {
			return errors.Wrapf(err, "invalid excludepatterns: %s", fs.opt.ExcludePatterns)
		}

		for _, p := range excludeMatcher.Patterns() {
			if p.Exclusion() && strings.ContainsAny(patternWithoutTrailingGlob(p), patternChars) {
				onlyPrefixExcludeExceptions = false
				break
			}
		}
	}

	type visitedDir struct {
		entry            gofs.DirEntry
		pathWithSep      string
		includeMatchInfo patternmatcher.MatchInfo
		excludeMatchInfo patternmatcher.MatchInfo
		calledFn         bool
	}

	// used only for include/exclude handling
	var parentDirs []visitedDir

	return fs.fs.Walk(ctx, target, func(path string, dirEntry gofs.DirEntry, walkErr error) (retErr error) {
		defer func() {
			if retErr != nil && isNotExist(retErr) {
				retErr = filepath.SkipDir
			}
		}()

		var (
			dir   visitedDir
			isDir bool
		)
		if dirEntry != nil {
			isDir = dirEntry.IsDir()
		}

		if includeMatcher != nil || excludeMatcher != nil {
			for len(parentDirs) != 0 {
				lastParentDir := parentDirs[len(parentDirs)-1].pathWithSep
				if strings.HasPrefix(path, lastParentDir) {
					break
				}
				parentDirs = parentDirs[:len(parentDirs)-1]
			}

			if isDir {
				dir = visitedDir{
					entry:       dirEntry,
					pathWithSep: path + string(filepath.Separator),
				}
			}
		}

		skip := false

		if includeMatcher != nil {
			var parentIncludeMatchInfo patternmatcher.MatchInfo
			if len(parentDirs) != 0 {
				parentIncludeMatchInfo = parentDirs[len(parentDirs)-1].includeMatchInfo
			}
			m, matchInfo, err := includeMatcher.MatchesUsingParentResults(path, parentIncludeMatchInfo)
			if err != nil {
				return errors.Wrap(err, "failed to match includepatterns")
			}

			if isDir {
				dir.includeMatchInfo = matchInfo
			}

			if !m {
				if isDir && onlyPrefixIncludes {
					// Optimization: we can skip walking this dir if no include
					// patterns could match anything inside it.
					dirSlash := path + string(filepath.Separator)
					for _, pat := range includeMatcher.Patterns() {
						if pat.Exclusion() {
							continue
						}
						patStr := patternWithoutTrailingGlob(pat) + string(filepath.Separator)
						if strings.HasPrefix(patStr, dirSlash) {
							goto passedIncludeFilter
						}
					}
					return filepath.SkipDir
				}
			passedIncludeFilter:
				skip = true
			}
		}

		if excludeMatcher != nil {
			var parentExcludeMatchInfo patternmatcher.MatchInfo
			if len(parentDirs) != 0 {
				parentExcludeMatchInfo = parentDirs[len(parentDirs)-1].excludeMatchInfo
			}
			m, matchInfo, err := excludeMatcher.MatchesUsingParentResults(path, parentExcludeMatchInfo)
			if err != nil {
				return errors.Wrap(err, "failed to match excludepatterns")
			}

			if isDir {
				dir.excludeMatchInfo = matchInfo
			}

			if m {
				if isDir && onlyPrefixExcludeExceptions {
					// Optimization: we can skip walking this dir if no
					// exceptions to exclude patterns could match anything
					// inside it.
					if !excludeMatcher.Exclusions() {
						return filepath.SkipDir
					}

					dirSlash := path + string(filepath.Separator)
					for _, pat := range excludeMatcher.Patterns() {
						if !pat.Exclusion() {
							continue
						}
						patStr := patternWithoutTrailingGlob(pat) + string(filepath.Separator)
						if strings.HasPrefix(patStr, dirSlash) {
							goto passedExcludeFilter
						}
					}
					return filepath.SkipDir
				}
			passedExcludeFilter:
				skip = true
			}
		}

		if walkErr != nil {
			if skip && errors.Is(walkErr, os.ErrPermission) {
				return nil
			}
			return walkErr
		}

		if includeMatcher != nil || excludeMatcher != nil {
			defer func() {
				if isDir {
					parentDirs = append(parentDirs, dir)
				}
			}()
		}

		if skip {
			return nil
		}

		dir.calledFn = true

		fi, err := dirEntry.Info()
		if err != nil {
			return err
		}
		stat, ok := fi.Sys().(*types.Stat)
		if !ok {
			return errors.WithStack(&os.PathError{Path: path, Err: syscall.EBADMSG, Op: "fileinfo without stat info"})
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if fs.opt != nil && fs.opt.Map != nil {
				result := fs.opt.Map(stat.Path, stat)
				if result == MapResultSkipDir {
					return filepath.SkipDir
				} else if result == MapResultExclude {
					return nil
				}
			}
			for i, parentDir := range parentDirs {
				if parentDir.calledFn {
					continue
				}
				parentFi, err := parentDir.entry.Info()
				if err != nil {
					return err
				}
				parentStat, ok := parentFi.Sys().(*types.Stat)
				if !ok {
					return errors.WithStack(&os.PathError{Path: path, Err: syscall.EBADMSG, Op: "fileinfo without stat info"})
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				if fs.opt != nil && fs.opt.Map != nil {
					result := fs.opt.Map(parentStat.Path, parentStat)
					if result == MapResultSkipDir || result == MapResultExclude {
						continue
					}
				}

				if err := fn(parentStat.Path, &DirEntryInfo{Stat: parentStat}, nil); err != nil {
					return err
				}
				parentDirs[i].calledFn = true
			}
			if err := fn(stat.Path, &DirEntryInfo{Stat: stat}, nil); err != nil {
				return err
			}
		}
		return nil
	})
}

func Walk(ctx context.Context, p string, opt *FilterOpt, fn filepath.WalkFunc) error {
	return NewFilterFS(NewFS(p), opt).Walk(ctx, "/", func(path string, d gofs.DirEntry, err error) error {
		var info gofs.FileInfo
		if d != nil {
			var err2 error
			info, err2 = d.Info()
			if err == nil {
				err = err2
			}
		}
		return fn(path, info, err)
	})
}

func WalkDir(ctx context.Context, p string, opt *FilterOpt, fn gofs.WalkDirFunc) error {
	return NewFilterFS(NewFS(p), opt).Walk(ctx, "/", fn)
}

func patternWithoutTrailingGlob(p *patternmatcher.Pattern) string {
	patStr := p.String()
	// We use filepath.Separator here because patternmatcher.Pattern patterns
	// get transformed to use the native path separator:
	// https://github.com/moby/patternmatcher/blob/130b41bafc16209dc1b52a103fdac1decad04f1a/patternmatcher.go#L52
	patStr = strings.TrimSuffix(patStr, string(filepath.Separator)+"**")
	patStr = strings.TrimSuffix(patStr, string(filepath.Separator)+"*")
	return patStr
}

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
