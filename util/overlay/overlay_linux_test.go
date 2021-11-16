//go:build linux
// +build linux

package overlay

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/continuity/fs"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/pkg/errors"
)

// This test file contains tests that are required in continuity project.
// (https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go)
// Most of them are ported from that project and patched to test our
// overlayfs-optimized differ.

// TestSimpleDiff is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L46-L73
// Copyright The containerd Authors.
func TestSimpleDiff(t *testing.T) {
	l1 := fstest.Apply(
		fstest.CreateDir("/etc", 0755),
		fstest.CreateFile("/etc/hosts", []byte("mydomain 10.0.0.1"), 0644),
		fstest.CreateFile("/etc/profile", []byte("PATH=/usr/bin"), 0644),
		fstest.CreateFile("/etc/unchanged", []byte("PATH=/usr/bin"), 0644),
		fstest.CreateFile("/etc/unexpected", []byte("#!/bin/sh"), 0644),
	)
	l2 := fstest.Apply(
		fstest.CreateFile("/etc/hosts", []byte("mydomain 10.0.0.120"), 0644),
		fstest.CreateFile("/etc/profile", []byte("PATH=/usr/bin"), 0666),
		fstest.CreateDir("/root", 0700),
		fstest.CreateFile("/root/.bashrc", []byte("PATH=/usr/sbin:/usr/bin"), 0644),
		fstest.Remove("/etc/unexpected"),
	)
	diff := []TestChange{
		Modify("/etc/hosts"),
		Modify("/etc/profile"),
		Delete("/etc/unexpected"),
		Add("/root"),
		Add("/root/.bashrc"),
	}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestEmptyFileDiff is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L75-L89
// Copyright The containerd Authors.
func TestEmptyFileDiff(t *testing.T) {
	tt := time.Now().Truncate(time.Second)
	l1 := fstest.Apply(
		fstest.CreateDir("/etc", 0755),
		fstest.CreateFile("/etc/empty", []byte(""), 0644),
		fstest.Chtimes("/etc/empty", tt, tt),
	)
	l2 := fstest.Apply()
	diff := []TestChange{}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestNestedDeletion is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L91-L111
// Copyright The containerd Authors.
func TestNestedDeletion(t *testing.T) {
	l1 := fstest.Apply(
		fstest.CreateDir("/d0", 0755),
		fstest.CreateDir("/d1", 0755),
		fstest.CreateDir("/d1/d2", 0755),
		fstest.CreateFile("/d1/d2/f1", []byte("mydomain 10.0.0.1"), 0644),
	)
	l2 := fstest.Apply(
		fstest.RemoveAll("/d0"),
		fstest.RemoveAll("/d1"),
	)
	diff := []TestChange{
		Delete("/d0"),
		Delete("/d1"),
	}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestDirectoryReplace is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L113-L134
// Copyright The containerd Authors.
func TestDirectoryReplace(t *testing.T) {
	l1 := fstest.Apply(
		fstest.CreateDir("/dir1", 0755),
		fstest.CreateFile("/dir1/f1", []byte("#####"), 0644),
		fstest.CreateDir("/dir1/f2", 0755),
		fstest.CreateFile("/dir1/f2/f3", []byte("#!/bin/sh"), 0644),
	)
	l2 := fstest.Apply(
		fstest.CreateFile("/dir1/f11", []byte("#New file here"), 0644),
		fstest.RemoveAll("/dir1/f2"),
		fstest.CreateFile("/dir1/f2", []byte("Now file"), 0666),
	)
	diff := []TestChange{
		Add("/dir1/f11"),
		Modify("/dir1/f2"),
	}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestRemoveDirectoryTree is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L136-L152
// Copyright The containerd Authors.
func TestRemoveDirectoryTree(t *testing.T) {
	l1 := fstest.Apply(
		fstest.CreateDir("/dir1/dir2/dir3", 0755),
		fstest.CreateFile("/dir1/f1", []byte("f1"), 0644),
		fstest.CreateFile("/dir1/dir2/f2", []byte("f2"), 0644),
	)
	l2 := fstest.Apply(
		fstest.RemoveAll("/dir1"),
	)
	diff := []TestChange{
		Delete("/dir1"),
	}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestRemoveDirectoryTreeWithDash is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L154-L172
// Copyright The containerd Authors.
func TestRemoveDirectoryTreeWithDash(t *testing.T) {
	l1 := fstest.Apply(
		fstest.CreateDir("/dir1/dir2/dir3", 0755),
		fstest.CreateFile("/dir1/f1", []byte("f1"), 0644),
		fstest.CreateFile("/dir1/dir2/f2", []byte("f2"), 0644),
		fstest.CreateDir("/dir1-before", 0755),
		fstest.CreateFile("/dir1-before/f2", []byte("f2"), 0644),
	)
	l2 := fstest.Apply(
		fstest.RemoveAll("/dir1"),
	)
	diff := []TestChange{
		Delete("/dir1"),
	}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestFileReplace is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L174-L192
// Copyright The containerd Authors.
func TestFileReplace(t *testing.T) {
	l1 := fstest.Apply(
		fstest.CreateFile("/dir1", []byte("a file, not a directory"), 0644),
	)
	l2 := fstest.Apply(
		fstest.Remove("/dir1"),
		fstest.CreateDir("/dir1/dir2", 0755),
		fstest.CreateFile("/dir1/dir2/f1", []byte("also a file"), 0644),
	)
	diff := []TestChange{
		Modify("/dir1"),
		Add("/dir1/dir2"),
		Add("/dir1/dir2/f1"),
	}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestParentDirectoryPermission is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L194-L219
// Copyright The containerd Authors.
func TestParentDirectoryPermission(t *testing.T) {
	l1 := fstest.Apply(
		fstest.CreateDir("/dir1", 0700),
		fstest.CreateDir("/dir2", 0751),
		fstest.CreateDir("/dir3", 0777),
	)
	l2 := fstest.Apply(
		fstest.CreateDir("/dir1/d", 0700),
		fstest.CreateFile("/dir1/d/f", []byte("irrelevant"), 0644),
		fstest.CreateFile("/dir1/f", []byte("irrelevant"), 0644),
		fstest.CreateFile("/dir2/f", []byte("irrelevant"), 0644),
		fstest.CreateFile("/dir3/f", []byte("irrelevant"), 0644),
	)
	diff := []TestChange{
		Add("/dir1/d"),
		Add("/dir1/d/f"),
		Add("/dir1/f"),
		Add("/dir2/f"),
		Add("/dir3/f"),
	}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestUpdateWithSameTime is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L221-L269
// Copyright The containerd Authors.
//
// NOTE: This test is patched for our differ. See the following NOTE for details.
func TestUpdateWithSameTime(t *testing.T) {
	tt := time.Now().Truncate(time.Second)
	t1 := tt.Add(5 * time.Nanosecond)
	t2 := tt.Add(6 * time.Nanosecond)
	l1 := fstest.Apply(
		fstest.CreateFile("/file-modified-time", []byte("1"), 0644),
		fstest.Chtimes("/file-modified-time", t1, t1),
		fstest.CreateFile("/file-no-change", []byte("1"), 0644),
		fstest.Chtimes("/file-no-change", t1, t1),
		fstest.CreateFile("/file-same-time", []byte("1"), 0644),
		fstest.Chtimes("/file-same-time", t1, t1),
		fstest.CreateFile("/file-truncated-time-1", []byte("1"), 0644),
		fstest.Chtimes("/file-truncated-time-1", tt, tt),
		fstest.CreateFile("/file-truncated-time-2", []byte("1"), 0644),
		fstest.Chtimes("/file-truncated-time-2", tt, tt),
		fstest.CreateFile("/file-truncated-time-3", []byte("1"), 0644),
		fstest.Chtimes("/file-truncated-time-3", t1, t1),
	)
	l2 := fstest.Apply(
		fstest.CreateFile("/file-modified-time", []byte("2"), 0644),
		fstest.Chtimes("/file-modified-time", t2, t2),
		fstest.CreateFile("/file-no-change", []byte("1"), 0644),
		fstest.Chtimes("/file-no-change", t1, t1),
		fstest.CreateFile("/file-same-time", []byte("2"), 0644),
		fstest.Chtimes("/file-same-time", t1, t1),
		fstest.CreateFile("/file-truncated-time-1", []byte("1"), 0644),
		fstest.Chtimes("/file-truncated-time-1", t1, t1),
		fstest.CreateFile("/file-truncated-time-2", []byte("2"), 0644),
		fstest.Chtimes("/file-truncated-time-2", tt, tt),
		fstest.CreateFile("/file-truncated-time-3", []byte("1"), 0644),
		fstest.Chtimes("/file-truncated-time-3", tt, tt),
	)
	diff := []TestChange{
		Modify("/file-modified-time"),

		// NOTE: Even if the file is identical, overlayfs copies it to
		//       the upper layer when the modification occurred. continuity's differ avoids counting
		//       this as "modify" by comparing the time and the file contents between upper and lower
		//       but here we want to avoid comparing bits which makes the differ slower.
		// TODO: we need a way to effectively determine two files are identical
		//       without copmaring bits.
		Modify("/file-no-change"),
		Modify("/file-same-time"),

		// Include changes with truncated timestamps. Comparing newly
		// extracted tars which have truncated timestamps will be
		// expected to produce changes. The expectation is that diff
		// archives are generated once and kept, newly generated diffs
		// will not consider cases where only one side is truncated.
		Modify("/file-truncated-time-1"),
		Modify("/file-truncated-time-2"),
		Modify("/file-truncated-time-3"),
	}

	if err := testDiffWithBase(l1, l2, diff); err != nil {
		t.Fatalf("Failed diff with base: %+v", err)
	}
}

// TestLchtimes is a test ported from
// https://github.com/containerd/continuity/blob/v0.1.0/fs/diff_test.go#L271-L291
// Copyright The containerd Authors.
// buildkit#172
func TestLchtimes(t *testing.T) {
	mtimes := []time.Time{
		time.Unix(0, 0),  // nsec is 0
		time.Unix(0, 42), // nsec > 0
	}
	for _, mtime := range mtimes {
		atime := time.Unix(424242, 42)
		l1 := fstest.Apply(
			fstest.CreateFile("/foo", []byte("foo"), 0644),
			fstest.Symlink("/foo", "/lnk0"),
			fstest.Lchtimes("/lnk0", atime, mtime),
		)
		l2 := fstest.Apply() // empty
		diff := []TestChange{}
		if err := testDiffWithBase(l1, l2, diff); err != nil {
			t.Fatalf("Failed diff with base: %+v", err)
		}
	}
}

func testDiffWithBase(base, diff fstest.Applier, expected []TestChange) error {
	t1, err := ioutil.TempDir("", "diff-with-base-lower-")
	if err != nil {
		return errors.Wrap(err, "failed to create temp dir")
	}
	defer os.RemoveAll(t1)

	if err := base.Apply(t1); err != nil {
		return errors.Wrap(err, "failed to apply base filesystem")
	}

	tupper, err := ioutil.TempDir("", "diff-with-base-upperdir-")
	if err != nil {
		return errors.Wrap(err, "failed to create temp dir")
	}
	defer os.RemoveAll(tupper)

	workdir, err := ioutil.TempDir("", "diff-with-base-workdir-")
	if err != nil {
		return errors.Wrap(err, "failed to create temp dir")
	}
	defer os.RemoveAll(workdir)

	return mount.WithTempMount(context.Background(), []mount.Mount{
		{
			Type:    "overlay",
			Source:  "overlay",
			Options: []string{fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", t1, tupper, workdir)},
		},
	}, func(overlayRoot string) error {
		if err := diff.Apply(overlayRoot); err != nil {
			return errors.Wrapf(err, "failed to apply diff to overlayRoot")
		}
		if err := collectAndCheckChanges(t1, tupper, expected); err != nil {
			return errors.Wrap(err, "failed to collect changes")
		}
		return nil
	})
}

func checkChanges(root string, changes, expected []TestChange) error {
	if len(changes) != len(expected) {
		return errors.Errorf("Unexpected number of changes:\n%s", diffString(changes, expected))
	}
	for i := range changes {
		if changes[i].Path != expected[i].Path || changes[i].Kind != expected[i].Kind {
			return errors.Errorf("Unexpected change at %d:\n%s", i, diffString(changes, expected))
		}
		if changes[i].Kind != fs.ChangeKindDelete {
			filename := filepath.Join(root, changes[i].Path)
			efi, err := os.Stat(filename)
			if err != nil {
				return errors.Wrapf(err, "failed to stat %q", filename)
			}
			afi := changes[i].FileInfo
			if afi.Size() != efi.Size() {
				return errors.Errorf("Unexpected change size %d, %q has size %d", afi.Size(), filename, efi.Size())
			}
			if afi.Mode() != efi.Mode() {
				return errors.Errorf("Unexpected change mode %s, %q has mode %s", afi.Mode(), filename, efi.Mode())
			}
			if afi.ModTime() != efi.ModTime() {
				return errors.Errorf("Unexpected change modtime %s, %q has modtime %s", afi.ModTime(), filename, efi.ModTime())
			}
			if expected := filepath.Join(root, changes[i].Path); changes[i].Source != expected {
				return errors.Errorf("Unexpected source path %s, expected %s", changes[i].Source, expected)
			}
		}
	}

	return nil
}

type TestChange struct {
	Kind     fs.ChangeKind
	Path     string
	FileInfo os.FileInfo
	Source   string
}

func collectAndCheckChanges(base, upperdir string, expected []TestChange) error {
	ctx := context.Background()
	changes := []TestChange{}

	emptyLower, err := ioutil.TempDir("", "buildkit-test-emptylower") // empty directory used for the lower of diff view
	if err != nil {
		return errors.Wrapf(err, "failed to create temp dir")
	}
	defer os.Remove(emptyLower)
	upperView := []mount.Mount{
		{
			Type:    "overlay",
			Source:  "overlay",
			Options: []string{fmt.Sprintf("lowerdir=%s", strings.Join([]string{upperdir, emptyLower}, ":"))},
		},
	}
	return mount.WithTempMount(ctx, upperView, func(upperViewRoot string) error {
		if err := Changes(ctx, func(k fs.ChangeKind, p string, f os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			changes = append(changes, TestChange{
				Kind:     k,
				Path:     p,
				FileInfo: f,
				Source:   filepath.Join(upperViewRoot, p),
			})
			return nil
		}, upperdir, upperViewRoot, base); err != nil {
			return err
		}
		if err := checkChanges(upperViewRoot, changes, expected); err != nil {
			return errors.Wrapf(err, "change check falied")
		}
		return nil
	})
}

func diffString(c1, c2 []TestChange) string {
	return fmt.Sprintf("got(%d):\n%s\nexpected(%d):\n%s", len(c1), changesString(c1), len(c2), changesString(c2))

}

func changesString(c []TestChange) string {
	strs := make([]string, len(c))
	for i := range c {
		strs[i] = fmt.Sprintf("\t%s\t%s", c[i].Kind, c[i].Path)
	}
	return strings.Join(strs, "\n")
}

func Add(p string) TestChange {
	return TestChange{
		Kind: fs.ChangeKindAdd,
		Path: filepath.FromSlash(p),
	}
}

func Delete(p string) TestChange {
	return TestChange{
		Kind: fs.ChangeKindDelete,
		Path: filepath.FromSlash(p),
	}
}

func Modify(p string) TestChange {
	return TestChange{
		Kind: fs.ChangeKindModify,
		Path: filepath.FromSlash(p),
	}
}
