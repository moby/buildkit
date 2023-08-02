package fsutil

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	strings "strings"
	"syscall"

	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil/types"
)

func FollowLinks(fs FS, paths []string) ([]string, error) {
	r := &symlinkResolver{fs: fs, resolved: map[string]struct{}{}, cache: make(map[string][]os.DirEntry)}
	for _, p := range paths {
		if err := r.append(p); err != nil {
			return nil, err
		}
	}
	res := make([]string, 0, len(r.resolved))
	for r := range r.resolved {
		res = append(res, filepath.ToSlash(r))
	}
	sort.Strings(res)
	return dedupePaths(res), nil
}

type symlinkResolver struct {
	fs       FS
	resolved map[string]struct{}
	cache    map[string][]os.DirEntry
}

func (r *symlinkResolver) append(p string) error {
	if runtime.GOOS == "windows" && filepath.IsAbs(filepath.FromSlash(p)) {
		absParts := strings.SplitN(p, ":", 2)
		if len(absParts) == 2 {
			p = absParts[1]
		}
	}
	p = filepath.Join(".", p)
	current := "."
	for {
		parts := strings.SplitN(p, string(filepath.Separator), 2)
		current = filepath.Join(current, parts[0])

		targets, err := r.readSymlink(current, true)
		if err != nil {
			return err
		}
		p = ""
		if len(parts) == 2 {
			p = parts[1]
		}

		if p == "" || targets != nil {
			if _, ok := r.resolved[current]; ok {
				return nil
			}
		}

		if targets != nil {
			r.resolved[current] = struct{}{}
			for _, target := range targets {
				if err := r.append(filepath.Join(target, p)); err != nil {
					return err
				}
			}
			return nil
		}

		if p == "" {
			r.resolved[current] = struct{}{}
			return nil
		}
	}
}

func (r *symlinkResolver) readSymlink(p string, allowWildcard bool) ([]string, error) {
	base := filepath.Base(p)
	if allowWildcard && containsWildcards(base) {
		fis, err := r.readDir(filepath.Dir(p))
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, errors.Wrap(err, "readdir")
		}
		var out []string
		for _, f := range fis {
			if ok, _ := filepath.Match(base, f.Name()); ok {
				res, err := r.readSymlink(filepath.Join(filepath.Dir(p), f.Name()), false)
				if err != nil {
					return nil, err
				}
				out = append(out, res...)
			}
		}
		return out, nil
	}

	entry, err := r.readFile(p)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, errors.WithStack(err)
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return nil, nil
	}

	fi, err := entry.Info()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	stat, ok := fi.Sys().(*types.Stat)
	if !ok {
		return nil, errors.WithStack(&os.PathError{Path: p, Err: syscall.EBADMSG, Op: "fileinfo without stat info"})
	}

	link := filepath.Clean(stat.Linkname)
	if filepath.IsAbs(link) {
		return []string{link}, nil
	}
	return []string{
		filepath.Join(string(filepath.Separator), filepath.Join(filepath.Dir(p), link)),
	}, nil
}

func (r *symlinkResolver) readFile(root string) (os.DirEntry, error) {
	out, err := r.readDir(filepath.Dir(root))
	if err != nil {
		return nil, err
	}
	for _, f := range out {
		if f.Name() == filepath.Base(root) {
			return f, nil
		}
	}
	return nil, os.ErrNotExist
}

func (r *symlinkResolver) readDir(root string) ([]os.DirEntry, error) {
	if entries, ok := r.cache[root]; ok {
		return entries, nil
	}

	var out []os.DirEntry
	err := r.fs.Walk(context.TODO(), root, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(root, p); err != nil || rel == "." {
			return err
		}
		out = append(out, entry)
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.cache[root] = out
	return out, nil
}

func containsWildcards(name string) bool {
	isWindows := runtime.GOOS == "windows"
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if ch == '\\' && !isWindows {
			i++
		} else if ch == '*' || ch == '?' || ch == '[' {
			return true
		}
	}
	return false
}

// dedupePaths expects input as a sorted list
func dedupePaths(in []string) []string {
	out := make([]string, 0, len(in))
	var last string
	for _, s := range in {
		// if one of the paths is root there is no filter
		if s == "." {
			return nil
		}
		if strings.HasPrefix(s, last+"/") {
			continue
		}
		out = append(out, s)
		last = s
	}
	return out
}
