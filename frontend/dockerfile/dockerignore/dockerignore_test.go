package dockerignore

import (
	"strings"
	"testing"
)

func TestReadAll(t *testing.T) {
	actual, err := ReadAll(nil)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if entries := len(actual); entries != 0 {
		t.Fatalf("Expected to have zero entries, got %d", entries)
	}

	const content = `test1
/test2
/a/file/here

lastfile
# this is a comment
! /inverted/abs/path
!
! `

	expected := []string{
		"test1",
		"test2",       // according to https://docs.docker.com/engine/reference/builder/#dockerignore-file, /foo/bar should be treated as foo/bar
		"a/file/here", // according to https://docs.docker.com/engine/reference/builder/#dockerignore-file, /foo/bar should be treated as foo/bar
		"lastfile",
		"!inverted/abs/path",
		"!",
		"!",
	}

	actual, err = ReadAll(strings.NewReader(content))
	if err != nil {
		t.Error(err)
	}

	if len(actual) != len(expected) {
		t.Errorf("Expected %d entries, got %v", len(expected), len(actual))
	}
	for i, expectedLine := range expected {
		if i >= len(actual) {
			t.Errorf(`missing line %d: expected: "%s", got none`, i+1, expectedLine)
			continue
		}
		if actual[i] != expectedLine {
			t.Errorf(`line %d: expected: "%s", got: "%s"`, i+1, expectedLine, actual[i])
		}
	}
}
