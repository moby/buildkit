package tar

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	gopath "path"
	fp "path/filepath"
	"strings"
)

type Extractor struct {
	Path     string
	Progress func(int64) int64

	// SanitizePathFunc can be provided if you wish to inspect and/or modify the source path
	// returning an error from this function will abort extraction
	SanitizePathFunc func(path string) (saferPath string, userDefined error)

	// LinkFunc can be provided for user specified handling of filesystem links
	// returning an error from this function aborts extraction
	LinkFunc func(Link) error
}

// Link represents a filesystem link where Name is the link's destination path,
// Target is what the link actually points to,
// and Root is the extraction root
type Link struct {
	Root, Name, Target string
}

func (te *Extractor) Extract(reader io.Reader) error {
	if isNullDevice(te.Path) {
		return nil
	}

	tarReader := tar.NewReader(reader)

	// Check if the output path already exists, so we know whether we should
	// create our output with that name, or if we should put the output inside
	// a preexisting directory
	rootExists := true
	rootIsDir := false
	if stat, err := os.Stat(te.Path); err != nil && os.IsNotExist(err) {
		rootExists = false
	} else if err != nil {
		return err
	} else if stat.IsDir() {
		rootIsDir = true
	}

	// files come recursively in order (i == 0 is root directory)
	for i := 0; ; i++ {
		header, err := tarReader.Next()
		if err != nil && err != io.EOF {
			return err
		}
		if header == nil || err == io.EOF {
			break
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := te.extractDir(header, i); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := te.extractFile(header, tarReader, i, rootExists, rootIsDir); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := te.extractSymlink(header); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unrecognized tar header type: %d", header.Typeflag)
		}
	}
	return nil
}

// Sanitize sets up the extractor to use built in sanitation functions
// (Modify paths to be platform legal, symlinks may not escape extraction root)
// or unsets any previously set sanitation functions on the extractor
// (no special rules are applied when extracting)
func (te *Extractor) Sanitize(toggle bool) {
	if toggle {
		te.SanitizePathFunc = sanitizePath
		te.LinkFunc = func(inLink Link) error {
			if err := childrenOnly(inLink); err != nil {
				return err
			}
			if err := platformLink(inLink); err != nil {
				return err
			}
			return os.Symlink(inLink.Target, inLink.Name)
		}
	} else {
		te.SanitizePathFunc = nil
		te.LinkFunc = nil
	}
}

// outputPath returns the path at which to place tarPath
func (te *Extractor) outputPath(tarPath string) (outPath string, err error) {
	elems := strings.Split(tarPath, "/")    // break into elems
	elems = elems[1:]                       // remove original root
	outPath = strings.Join(elems, "/")      // join elems
	outPath = gopath.Join(te.Path, outPath) // rebase on to extraction target root
	// sanitize path to be platform legal
	if te.SanitizePathFunc != nil {
		outPath, err = te.SanitizePathFunc(outPath)
	} else {
		outPath = fp.FromSlash(outPath)
	}
	return
}

func (te *Extractor) extractDir(h *tar.Header, depth int) error {
	path, err := te.outputPath(h.Name)
	if err != nil {
		return err
	}

	if depth == 0 {
		// if this is the root directory, use it as the output path for remaining files
		te.Path = path
	}

	return os.MkdirAll(path, 0755)
}

func (te *Extractor) extractSymlink(h *tar.Header) error {
	path, err := te.outputPath(h.Name)
	if err != nil {
		return err
	}

	if te.LinkFunc != nil {
		return te.LinkFunc(Link{Root: te.Path, Name: h.Name, Target: h.Linkname})
	}

	return os.Symlink(h.Linkname, path)
}

func (te *Extractor) extractFile(h *tar.Header, r *tar.Reader, depth int, rootExists bool, rootIsDir bool) error {
	path, err := te.outputPath(h.Name)
	if err != nil {
		return err
	}

	if depth == 0 { // if depth is 0, this is the only file (we aren't extracting a directory)
		if rootExists && rootIsDir {
			// putting file inside of a root dir.
			fnameo := gopath.Base(h.Name)
			fnamen := fp.Base(path)
			// add back original name if lost.
			if fnameo != fnamen {
				path = fp.Join(path, fnameo)
			}
		} // else if old file exists, just overwrite it.
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	return copyWithProgress(file, r, te.Progress)
}

func copyWithProgress(to io.Writer, from io.Reader, cb func(int64) int64) error {
	buf := make([]byte, 4096)
	for {
		n, err := from.Read(buf)
		if n != 0 {
			if cb != nil {
				cb(int64(n))
			}
			_, err2 := to.Write(buf[:n])
			if err2 != nil {
				return err2
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// childrenOnly will return an error if link targets escape their root
func childrenOnly(inLink Link) error {
	if fp.IsAbs(inLink.Target) {
		return fmt.Errorf("Link target %q is an absolute path (forbidden)", inLink.Target)
	}

	resolvedTarget := fp.Join(inLink.Name, inLink.Target)
	rel, err := fp.Rel(inLink.Root, resolvedTarget)
	if err != nil {
		return err
	}
	//disallow symlinks from climbing out of the target root
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("Symlink target %q escapes root %q", inLink.Target, inLink.Root)
	}
	//disallow pointing to your own root from above as well
	if strings.HasPrefix(resolvedTarget, inLink.Root) {
		return fmt.Errorf("Symlink target %q escapes and re-enters its own root %q (forbidden)", inLink.Target, inLink.Root)
	}

	return nil
}
