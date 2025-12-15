package build

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/containerd/console"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session/filesync"
	"github.com/pkg/errors"
	"github.com/tonistiigi/go-csvvalue"
)

// parseOutputCSV parses a single --output CSV string
func parseOutputCSV(s string) (client.ExportEntry, error) {
	ex := client.ExportEntry{
		Type:  "",
		Attrs: map[string]string{},
	}
	fields, err := csvvalue.Fields(s, nil)
	if err != nil {
		return ex, err
	}
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return ex, errors.Errorf("invalid value %s", field)
		}
		key = strings.ToLower(key)
		switch key {
		case "type":
			ex.Type = value
		default:
			ex.Attrs[key] = value
		}
	}
	if ex.Type == "" {
		return ex, errors.New("--output requires type=<type>")
	}
	if v, ok := ex.Attrs["output"]; ok {
		return ex, errors.Errorf("output=%s not supported for --output, you meant dest=%s?", v, v)
	}
	ex.Output, ex.OutputDir, err = resolveExporterDest(ex.Type, ex.Attrs)
	if err != nil {
		return ex, errors.Wrap(err, "invalid output option: output")
	}
	return ex, nil
}

// ParseOutput parses --output
func ParseOutput(exports []string) ([]client.ExportEntry, error) {
	var entries []client.ExportEntry
	for _, s := range exports {
		e, err := parseOutputCSV(s)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// resolveExporterDest returns at most either one of io.WriteCloser (single file) or a string (directory path).
func resolveExporterDest(exporter string, attrs map[string]string) (destFile filesync.FileOutputFunc, destDir string, _ error) {
	var destFilePath string
	switch exporter {
	case client.ExporterLocal:
		var ok bool
		destDir, ok = attrs["dest"]
		if !ok {
			return nil, "", errors.Errorf("%s exporter requires dest=<path>", client.ExporterLocal)
		}
		delete(attrs, "dest")
	case client.ExporterTar:
		var ok bool
		destFilePath, ok = attrs["dest"]
		if !ok {
			return nil, "", errors.Errorf("%s exporter requires dest=<file>", client.ExporterTar)
		}
		delete(attrs, "dest")
	case client.ExporterOCI, client.ExporterDocker:
		tar, err := strconv.ParseBool(attrs["tar"])
		if err != nil {
			tar = true
		}
		var ok bool
		if tar {
			destFilePath, ok = attrs["dest"]
		} else {
			destDir, ok = attrs["dest"]
		}
		if !ok {
			return nil, "", errors.Errorf("%s exporter requires dest=<file|path>", exporter)
		}
		delete(attrs, "dest")
	case client.ExporterGateway:
		destFilePath = attrs["file"]
		destDir = attrs["dir"]
		if destFilePath == "" && destDir == "" {
			return nil, "", errors.Errorf("%s exporter requires file=<file> or dir=<path>", exporter)
		}
		if destFilePath != "" && destDir != "" {
			return nil, "", errors.Errorf("%s exporter requires only one of file=<file> or dir=<path>", exporter)
		}
		delete(attrs, "file")
		delete(attrs, "dir")
	default:
		dest, ok := attrs["dest"]
		if ok {
			return nil, "", errors.Errorf("output %s is not supported by %s exporter", dest, exporter)
		}
	}

	if destFilePath != "" {
		wrapWriter := func(wc io.WriteCloser) func(map[string]string) (io.WriteCloser, error) {
			return func(m map[string]string) (io.WriteCloser, error) {
				return wc, nil
			}
		}
		if destFilePath != "" && destFilePath != "-" {
			fi, err := os.Stat(destFilePath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, "", errors.Wrapf(err, "invalid destination file: %s", destFilePath)
			}
			if err == nil && fi.IsDir() {
				return nil, "", errors.Errorf("destination file is a directory")
			}
			w, err := os.Create(destFilePath)
			if err != nil {
				return nil, "", err
			}
			destFile = wrapWriter(w)
		} else {
			// if no output file is specified, use stdout
			if _, err := console.ConsoleFromFile(os.Stdout); err == nil {
				return nil, "", errors.Errorf("output file is required for %s exporter. refusing to write to console", exporter)
			}
			destFile = wrapWriter(os.Stdout)
		}
	}

	return destFile, destDir, nil
}
