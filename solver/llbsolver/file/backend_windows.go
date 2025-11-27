package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/util/windows"
	"github.com/moby/sys/user"
	copy "github.com/tonistiigi/fsutil/copy"
)

// windowsProtectedFiles contains Windows system files/folders that must be skipped
// during copy operations to avoid "Access denied" errors.
var windowsProtectedFiles = map[string]struct{}{
	"system volume information": {},
	"wcsandboxstate":            {},
}

func mapUserToChowner(user *copy.User, _ *user.IdentityMapping) (copy.Chowner, error) {
	if user == nil || user.SID == "" {
		return func(old *copy.User) (*copy.User, error) {
			if old == nil || old.SID == "" {
				old = &copy.User{
					SID: windows.ContainerAdministratorSidString,
				}
			}
			return old, nil
		}, nil
	}
	return func(*copy.User) (*copy.User, error) {
		return user, nil
	}, nil
}

// platformCopy wraps copy.Copy to handle Windows protected system folders.
// On Windows, container snapshots mounted to the host filesystem include protected folders
// ("System Volume Information" and "WcSandboxState") at the mount root, which cause "Access is denied"
// errors. When copying from the mount root, we manually enumerate and skip these folders.
func platformCopy(ctx context.Context, srcRoot string, src string, destRoot string, dest string, opt ...copy.Opt) error {
	// Resolve the source path to check if we're copying from the mount root
	srcPath, err := fs.RootPath(srcRoot, src)
	if err != nil {
		return err
	}

	// Check if copying from mount root where protected folders exist
	if filepath.Clean(srcPath) == filepath.Clean(srcRoot) {
		// Check if any protected files exist (indicates a Windows mount root)
		for protectedFile := range windowsProtectedFiles {
			protectedPath := filepath.Join(srcRoot, protectedFile)
			if _, err := os.Stat(protectedPath); err == nil {
				// Use manual enumeration to skip protected folders
				return copyByEnumeratingChildren(ctx, srcRoot, src, destRoot, dest, opt...)
			}
		}
	}
	// Normal case - use standard copy
	return copy.Copy(ctx, srcRoot, src, destRoot, dest, opt...)
}

// copyByEnumeratingChildren manually enumerates the root directory and copies each child,
// skipping Windows protected files. This is necessary because copy.Copy calls os.Lstat
// before checking exclude patterns, which causes "Access denied" errors.
//
// When IncludePatterns are present, this function adjusts them to be relative to each
// child being copied. For example, if copying from "/" with pattern "test/a/b/c/d",
// when copying the "test" child, the pattern is adjusted to "a/b/c/d".
func copyByEnumeratingChildren(ctx context.Context, srcRoot string, src string, destRoot string, dest string, opt ...copy.Opt) error {
	// Extract CopyInfo to access IncludePatterns that control which files to copy
	ci := copy.CopyInfo{}
	for _, o := range opt {
		o(&ci)
	}

	// Resolve the actual filesystem path we're copying from
	srcPath, err := fs.RootPath(srcRoot, src)
	if err != nil {
		return err
	}

	// Enumerate all entries at the root level (before os.Lstat can fail on protected files)
	entries, err := os.ReadDir(srcPath)
	if err != nil {
		return err
	}

	// Resolve destination path
	destPath, err := fs.RootPath(destRoot, dest)
	if err != nil {
		return err
	}

	// Create the destination directory with same permissions as source
	srcInfo, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}
	if srcInfo.IsDir() {
		if err := os.MkdirAll(destPath, srcInfo.Mode()); err != nil && !os.IsExist(err) {
			return err
		}
	}

	// Process each child entry individually
	for _, entry := range entries {
		name := entry.Name()

		// Skip protected files that would cause "Access denied" errors
		if _, isProtected := windowsProtectedFiles[strings.ToLower(name)]; isProtected {
			continue
		}

		// Build source and destination paths for this child
		// Handle special case where src is root ("/", ".", or "")
		childSrc := filepath.Join(src, name)
		if src == "/" || src == "." || src == "" {
			childSrc = "/" + name
		}
		childDest := filepath.Join(dest, name)

		// Adjust patterns to be relative to this child's path
		// E.g., pattern "test/a/b" becomes "a/b" when copying child "test"
		adjustedIncludePatterns := adjustIncludePatternsForChild(ci.IncludePatterns, name)

		// Determine whether to copy this child based on patterns
		var childOpts []copy.Opt
		if len(adjustedIncludePatterns) > 0 {
			// Patterns match this child - use adjusted patterns
			childCi := ci
			childCi.IncludePatterns = adjustedIncludePatterns
			childOpts = append(childOpts, copy.WithCopyInfo(childCi))
		} else if len(ci.IncludePatterns) == 0 {
			// No filtering needed - copy everything
			childOpts = opt
		} else {
			// Patterns specified but don't match this child - skip it
			continue
		}

		// Recursively copy this child using standard copy.Copy
		// (safe now because we've already enumerated past the protected files)
		if err := copy.Copy(ctx, srcRoot, childSrc, destRoot, childDest, childOpts...); err != nil {
			return err
		}
	}

	return nil
}

// adjustIncludePatternsForChild adjusts include patterns to be relative to a child directory.
// When copying from "/" with pattern "test/a/b/c" and processing child "test",
// the pattern is adjusted to "a/b/c" so it matches correctly under the new source root.
func adjustIncludePatternsForChild(patterns []string, childName string) []string {
	var adjusted []string
	prefix := childName + "/"

	for _, pattern := range patterns {
		// Remove the child name prefix from the pattern
		// Example: "test/a/b/c" → "a/b/c" for child "test"
		if adjustedPattern, ok := strings.CutPrefix(pattern, prefix); ok {
			adjusted = append(adjusted, adjustedPattern)
		} else if pattern == childName {
			// Pattern exactly matches this child, include everything under it
			adjusted = append(adjusted, "**")
		}
	}

	return adjusted
}
