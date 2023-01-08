// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcpopt

import (
	"fmt"
	"sync"
)

var parserMu sync.RWMutex

// Register registers a socket option parser.
func Register(level, name int, fn func([]byte) (Option, error)) {
	parserMu.Lock()
	parsers[int64(level)<<32|int64(name)] = fn
	parserMu.Unlock()
}

// Unregister unregisters a socket option parser.
func Unregister(level, name int) {
	parserMu.Lock()
	delete(parsers, int64(level)<<32|int64(name))
	parserMu.Unlock()
}

// Parse parses a socket option.
func Parse(level, name int, b []byte) (Option, error) {
	parserMu.RLock()
	defer parserMu.RUnlock()
	fn, ok := parsers[int64(level)<<32|int64(name)]
	if !ok {
		return nil, fmt.Errorf("parser for level=%#x name=%#x not found", level, name)
	}
	return fn(b)
}
