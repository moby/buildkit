// Copyright (C) 2014 Space Monkey, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package utils

import (
	"sync"
)

// Future is a type that is essentially the inverse of a channel. With a
// channel, you have multiple senders and one receiver. With a future, you can
// have multiple receivers and one sender. Additionally, a future protects
// against double-sends. Since this is usually used for returning function
// results, we also capture and return error values as well. Use NewFuture
// to initialize.
type Future struct {
	mutex    *sync.Mutex
	cond     *sync.Cond
	received bool
	val      interface{}
	err      error
}

// NewFuture returns an initialized and ready Future.
func NewFuture() *Future {
	mutex := &sync.Mutex{}
	return &Future{
		mutex:    mutex,
		cond:     sync.NewCond(mutex),
		received: false,
		val:      nil,
		err:      nil,
	}
}

// Get blocks until the Future has a value set.
func (f *Future) Get() (interface{}, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	for {
		if f.received {
			return f.val, f.err
		}
		f.cond.Wait()
	}
}

// Fired returns whether or not a value has been set. If Fired is true, Get
// won't block.
func (f *Future) Fired() bool {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.received
}

// Set provides the value to present and future Get calls. If Set has already
// been called, this is a no-op.
func (f *Future) Set(val interface{}, err error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.received {
		return
	}
	f.received = true
	f.val = val
	f.err = err
	f.cond.Broadcast()
}
