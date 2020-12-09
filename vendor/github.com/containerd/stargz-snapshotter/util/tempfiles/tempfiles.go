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

package tempfiles

import (
	"io/ioutil"
	"os"
	"sync"

	"github.com/hashicorp/go-multierror"
)

func NewTempFiles() *TempFiles {
	return &TempFiles{}
}

type TempFiles struct {
	files   []*os.File
	filesMu sync.Mutex
}

func (tf *TempFiles) TempFile(dir, pattern string) (*os.File, error) {
	f, err := ioutil.TempFile(dir, pattern)
	if err != nil {
		return nil, err
	}
	tf.filesMu.Lock()
	tf.files = append(tf.files, f)
	tf.filesMu.Unlock()
	return f, nil
}

func (tf *TempFiles) CleanupAll() (allErr error) {
	tf.filesMu.Lock()
	defer tf.filesMu.Unlock()
	for _, f := range tf.files {
		if err := f.Close(); err != nil {
			allErr = multierror.Append(allErr, err)
		}
		if err := os.Remove(f.Name()); err != nil {
			allErr = multierror.Append(allErr, err)
		}
	}
	tf.files = nil
	return nil
}
