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
	"github.com/moby/buildkit/util/tracing/delegated"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"go.opentelemetry.io/otel/trace"
)

// ResolveClient resolves a client from CLI args
func ResolveClient(c *cli.Context) (*client.Client, error) {
	serverName := c.GlobalString("tlsservername")
	if serverName == "" {
		// guess servername as hostname of target address
		uri, err := url.Parse(c.GlobalString("addr"))
		if err != nil {
			return nil, err
		}
		serverName = uri.Hostname()
	}

	var (
		caCert string
		cert   string
		key    string
		err    error
	)

	tlsDir := c.GlobalString("tlsdir")

	if tlsDir != "" {
		// Fail straight away if TLS was specified both ways
		if c.GlobalString("tlscacert") != "" || c.GlobalString("tlscert") != "" || c.GlobalString("tlskey") != "" {
			return nil, errors.New("cannot specify tlsdir and tlscacert/tlscert/tlskey at the same time")
		}

		caCert, cert, key, err = resolveTLSFilesFromDir(tlsDir)
		if err != nil {
			return nil, err
		}
	} else {
		caCert = c.GlobalString("tlscacert")
		cert = c.GlobalString("tlscert")
		key = c.GlobalString("tlskey")
	}

	ctx := CommandContext(c)
	var opts []client.ClientOpt
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		opts = append(opts,
			client.WithTracerProvider(span.TracerProvider()),
			client.WithTracerDelegate(delegated.DefaultExporter),
		)
	}

	if caCert != "" {
		opts = append(opts, client.WithServerConfig(serverName, caCert))
	}
	if cert != "" || key != "" {
		opts = append(opts, client.WithCredentials(cert, key))
	}

	timeout := time.Duration(c.GlobalInt("timeout")) * time.Second
	if timeout > 0 {
		ctx2, cancel := context.WithCancelCause(ctx)
		ctx2, _ = context.WithTimeoutCause(ctx2, timeout, errors.WithStack(context.DeadlineExceeded)) //nolint:govet
		ctx = ctx2
		defer func() { cancel(errors.WithStack(context.Canceled)) }()
	}

	cl, err := client.New(ctx, c.GlobalString("addr"), opts...)
	if err != nil {
		return nil, err
	}

	wait := c.GlobalBool("wait")
	if wait {
		if err := cl.Wait(ctx); err != nil {
			return nil, err
		}
	}

	return cl, nil
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
		"json": func(v any) string {
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

// resolveTLSFilesFromDir scans a TLS directory for known cert/key filenames.
func resolveTLSFilesFromDir(tlsDir string) (caCert, cert, key string, err error) {
	oneOf := func(either, or string) (string, error) {
		for _, name := range []string{either, or} {
			fpath := filepath.Join(tlsDir, name)
			if _, err := os.Stat(fpath); err == nil {
				return fpath, nil
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
		return "", errors.Errorf("directory did not contain one of the needed files: %s or %s", either, or)
	}

	if caCert, err = oneOf("ca.pem", "ca.crt"); err != nil {
		return "", "", "", err
	}
	if cert, err = oneOf("cert.pem", "tls.crt"); err != nil {
		return "", "", "", err
	}
	if key, err = oneOf("key.pem", "tls.key"); err != nil {
		return "", "", "", err
	}
	return caCert, cert, key, nil
}
