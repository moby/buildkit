//go:build go1.18
// +build go1.18

// Copyright 2017 The BuildKit Authors
// SPDX-License-Identifier: Apache-2.0

package buildkit_test

import (
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// FuzzDockerfileParser tests Dockerfile parsing with arbitrary
// attacker-controlled Dockerfile content.
//
// BuildKit processes Dockerfiles from user repositories. A parsing
// bug in the Dockerfile parser = supply chain compromise via
// malicious Dockerfiles.
//
// 7 GitHub Security Advisories exist for BuildKit.
func FuzzDockerfileParser(f *testing.F) {
	f.Add("FROM alpine:latest\nRUN echo hello")
	f.Add("FROM scratch\nCOPY . /app")
	f.Add("")
	f.Add("FROM")
	f.Add(strings.Repeat("# comment\n", 100))
	f.Add("FROM alpine\n" + strings.Repeat("RUN echo hi\n", 1000))

	f.Fuzz(func(t *testing.T, dockerfile string) {
		if len(dockerfile) > 1<<17 {
			return
		}

		// Parse must never panic
		_, _ = parser.Parse(strings.NewReader(dockerfile))
	})
}

// FuzzDockerfileDirectives tests directive parsing with arbitrary
// line data. Directives control parser behavior (#syntax=, #check=).
func FuzzDockerfileDirectives(f *testing.F) {
	f.Add([]byte("#syntax=docker/dockerfile:1"))
	f.Add([]byte("#check=skip=all"))
	f.Add([]byte(""))
	f.Add([]byte("FROM alpine"))

	f.Fuzz(func(t *testing.T, line []byte) {
		if len(line) > 1<<16 {
			return
		}

		var dp parser.DirectiveParser
		_, _ = dp.ParseLine(line)
	})
}

// FuzzParseHeredoc tests heredoc parsing with arbitrary string input.
// Heredocs are used for multi-line RUN/COPY instructions.
func FuzzParseHeredoc(f *testing.F) {
	f.Add("<<EOF\nhello\nEOF")
	f.Add("")
	f.Add("<<-EOF\n\tindented\nEOF")

	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<16 {
			return
		}
		_, _ = parser.ParseHeredoc(src)
	})
}
