package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/pkg/errors"
)

func main() {
	re := regexp.MustCompile("(?s)<!---GENERATE_START (.*?)-->(.*?)<!---GENERATE_END-->\n")

	root, err := os.OpenRoot("./docs")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}

		data, err := root.ReadFile(path)
		if err != nil {
			return err
		}

		dataNew := re.ReplaceAllFunc(data, func(match []byte) []byte {
			groups := re.FindStringSubmatch(string(match))
			stdout := bytes.NewBuffer(nil)
			fmt.Fprintf(stdout, "<!---GENERATE_START %s-->\n", groups[1])
			fmt.Fprint(stdout, "```\n")
			cmd := exec.Cmd{
				Path:   "/bin/sh",
				Args:   []string{"sh", "-c", groups[1]},
				Stdout: stdout,
			}
			err = cmd.Start()
			if err != nil {
				err = errors.Wrapf(err, "could not start command %s", groups[1])
				return nil
			}
			err = cmd.Wait()
			if err != nil {
				err = errors.Wrapf(err, "could not run command %s", groups[1])
				return nil
			}
			fmt.Fprint(stdout, "```\n")
			fmt.Fprint(stdout, "<!---GENERATE_END-->\n")

			return stdout.Bytes()
		})
		if err != nil {
			return err
		}

		if !bytes.Equal(data, dataNew) {
			info, err := d.Info()
			if err != nil {
				return err
			}
			fmt.Println(filepath.Join("docs", path))
			if err := root.WriteFile(path, dataNew, info.Mode()); err != nil {
				return err
			}
		}

		return nil
	})
	if closeErr := root.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
