package common

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/tracing/detect"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.opentelemetry.io/otel/trace"
)

// ResolveClient resolves a client from CLI args
func ResolveClient(clicontext *cli.Context) (*client.Client, error) {
	serverName := clicontext.String("tlsservername")
	if serverName == "" {
		// guess servername as hostname of target address
		uri, err := url.Parse(clicontext.String("addr"))
		if err != nil {
			return nil, err
		}
		serverName = uri.Hostname()
	}

	var caCert string
	var cert string
	var key string

	tlsDir := clicontext.String("tlsdir")

	if tlsDir != "" {
		// Look for ca.pem and, if it exists, set caCert to that
		// Look for cert.pem and, if it exists, set key to that
		// Look for key.pem and, if it exists, set tlsDir to that
		for _, v := range [3]string{"ca.pem", "cert.pem", "key.pem"} {
			file := filepath.Join(tlsDir, v)
			if _, err := os.Stat(file); err == nil {
				switch v {
				case "ca.pem":
					caCert = file
				case "cert.pem":
					cert = file
				case "key.pem":
					key = file
				}
			} else {
				return nil, err
			}
		}

		if clicontext.String("tlscacert") != "" || clicontext.String("tlscert") != "" || clicontext.String("tlskey") != "" {
			return nil, errors.New("cannot specify tlsdir and tlscacert/tlscert/tlskey at the same time")
		}
	} else {
		caCert = clicontext.String("tlscacert")
		cert = clicontext.String("tlscert")
		key = clicontext.String("tlskey")
	}

	opts := []client.ClientOpt{client.WithFailFast()}

	ctx := CommandContext(clicontext)

	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		opts = append(opts, client.WithTracerProvider(span.TracerProvider()))

		exp, err := detect.Exporter()
		if err != nil {
			return nil, err
		}

		if td, ok := exp.(client.TracerDelegate); ok {
			opts = append(opts, client.WithTracerDelegate(td))
		}
	}

	if caCert != "" || cert != "" || key != "" {
		opts = append(opts, client.WithCredentials(serverName, caCert, cert, key))
	}

	timeout := time.Duration(clicontext.Int("timeout"))
	ctx, cancel := context.WithTimeout(ctx, timeout*time.Second)
	defer cancel()

	return client.New(ctx, clicontext.String("addr"), opts...)
}

func ParseTemplate(format string) (*template.Template, error) {
	// aliases is from https://github.com/containerd/nerdctl/blob/v0.17.1/cmd/nerdctl/fmtutil.go#L116-L126 (Apache License 2.0)
	aliases := map[string]string{
		"json": "{{json .}}",
	}
	if alias, ok := aliases[format]; ok {
		format = alias
	}
	// funcs is from https://github.com/docker/cli/blob/v20.10.12/templates/templates.go#L12-L20 (Apache License 2.0)
	funcs := template.FuncMap{
		"json": func(v interface{}) string {
			buf := &bytes.Buffer{}
			enc := json.NewEncoder(buf)
			enc.SetEscapeHTML(false)
			enc.Encode(v)
			// Remove the trailing new line added by the encoder
			return strings.TrimSpace(buf.String())
		},
	}
	return template.New("").Funcs(funcs).Parse(format)
}
