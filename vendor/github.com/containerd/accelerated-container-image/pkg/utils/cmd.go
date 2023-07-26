/*
   Copyright The Accelerated Container Image Authors

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

package utils

import (
	"context"
	"os"
	"os/exec"
	"path"

	"github.com/pkg/errors"
)

const (
	obdBinCreate = "/opt/overlaybd/bin/overlaybd-create"
	obdBinCommit = "/opt/overlaybd/bin/overlaybd-commit"

	dataFile       = "writable_data"
	idxFile        = "writable_index"
	sealedFile     = "overlaybd.sealed"
	commitTempFile = "overlaybd.commit.temp"
	commitFile     = "overlaybd.commit"
)

func Create(ctx context.Context, dir string, opts ...string) error {
	dataPath := path.Join(dir, dataFile)
	indexPath := path.Join(dir, idxFile)
	os.RemoveAll(dataPath)
	os.RemoveAll(indexPath)
	args := append([]string{dataPath, indexPath}, opts...)
	out, err := exec.CommandContext(ctx, obdBinCreate, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-create: %s", out)
	}
	return nil
}

func Seal(ctx context.Context, dir, toDir string, opts ...string) error {
	args := append([]string{
		"--seal",
		path.Join(dir, dataFile),
		path.Join(dir, idxFile),
	}, opts...)
	out, err := exec.CommandContext(ctx, obdBinCommit, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to seal writable overlaybd: %s", out)
	}
	return os.Rename(path.Join(dir, dataFile), path.Join(toDir, sealedFile))
}

func Commit(ctx context.Context, dir, toDir string, sealed bool, opts ...string) error {
	var args []string
	if sealed {
		args = append([]string{
			"--commit_sealed",
			path.Join(dir, sealedFile),
			path.Join(toDir, commitTempFile),
		}, opts...)
	} else {
		args = append([]string{
			path.Join(dir, dataFile),
			path.Join(dir, idxFile),
			path.Join(toDir, commitFile),
		}, opts...)
	}
	out, err := exec.CommandContext(ctx, obdBinCommit, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-commit: %s", out)
	}
	if sealed {
		return os.Rename(path.Join(toDir, commitTempFile), path.Join(toDir, commitFile))
	}
	return nil
}
