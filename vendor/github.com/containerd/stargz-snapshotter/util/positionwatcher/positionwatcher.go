/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package positionwatcher

import (
	"fmt"
	"io"
	"sync"
)

func NewPositionWatcher(r io.ReaderAt) (*PositionWatcher, error) {
	if r == nil {
		return nil, fmt.Errorf("Target ReaderAt is empty")
	}
	pos := int64(0)
	return &PositionWatcher{r: r, cPos: &pos}, nil
}

type PositionWatcher struct {
	r    io.ReaderAt
	cPos *int64

	mu sync.Mutex
}

func (pwr *PositionWatcher) Read(p []byte) (int, error) {
	pwr.mu.Lock()
	defer pwr.mu.Unlock()

	n, err := pwr.r.ReadAt(p, *pwr.cPos)
	if err == nil {
		*pwr.cPos += int64(n)
	}
	return n, err
}

func (pwr *PositionWatcher) Seek(offset int64, whence int) (int64, error) {
	pwr.mu.Lock()
	defer pwr.mu.Unlock()

	switch whence {
	default:
		return 0, fmt.Errorf("Unknown whence: %v", whence)
	case io.SeekStart:
	case io.SeekCurrent:
		offset += *pwr.cPos
	case io.SeekEnd:
		return 0, fmt.Errorf("Unsupported whence: %v", whence)
	}

	if offset < 0 {
		return 0, fmt.Errorf("invalid offset")
	}
	*pwr.cPos = offset
	return offset, nil
}

func (pwr *PositionWatcher) CurrentPos() int64 {
	pwr.mu.Lock()
	defer pwr.mu.Unlock()

	return *pwr.cPos
}
