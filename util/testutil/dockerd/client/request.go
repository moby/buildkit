//nolint:forbidigo
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/pkg/errors"
)

func (cli *Client) buildRequest(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	req = cli.addHeaders(req, headers)
	req.URL.Scheme = cli.scheme
	req.URL.Host = cli.addr

	if cli.proto == "unix" || cli.proto == "npipe" {
		// Override host header for non-tcp connections.
		req.Host = DummyHost
	}

	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "text/plain")
	}
	return req, nil
}

func (cli *Client) doRequest(req *http.Request) (*http.Response, error) {
	resp, err := cli.client.Do(req)
	if err != nil {
		if cli.scheme != "https" && strings.Contains(err.Error(), "malformed HTTP response") {
			return nil, errors.Errorf("%v.\n* Are you trying to connect to a TLS-enabled daemon without TLS?", err)
		}

		if cli.scheme == "https" && strings.Contains(err.Error(), "bad certificate") {
			return nil, errors.Wrap(err, "the server probably has client authentication (--tlsverify) enabled; check your TLS client certification settings")
		}

		// Don't decorate context sentinel errors; users may be comparing to
		// them directly.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		uErr := &url.Error{}
		if errors.As(err, &uErr) {
			nErr := &net.OpError{}
			if errors.As(uErr.Err, &nErr) {
				if os.IsPermission(nErr.Err) {
					return nil, errors.Wrapf(err, "permission denied while trying to connect to the Docker daemon socket at %v", cli.host)
				}
			}
		}

		var nErr net.Error
		if errors.As(err, &nErr) {
			// FIXME(thaJeztah): any net.Error should be considered a connection error (but we should include the original error)?
			if nErr.Timeout() {
				return nil, ErrorConnectionFailed(cli.host)
			}
			if strings.Contains(nErr.Error(), "connection refused") || strings.Contains(nErr.Error(), "dial unix") {
				return nil, ErrorConnectionFailed(cli.host)
			}
		}

		// Although there's not a strongly typed error for this in go-winio,
		// lots of people are using the default configuration for the docker
		// daemon on Windows where the daemon is listening on a named pipe
		// `//./pipe/docker_engine, and the client must be running elevated.
		// Give users a clue rather than the not-overly useful message
		// such as `error during connect: Get http://%2F%2F.%2Fpipe%2Fdocker_engine/v1.26/info:
		// open //./pipe/docker_engine: The system cannot find the file specified.`.
		// Note we can't string compare "The system cannot find the file specified" as
		// this is localised - for example in French the error would be
		// `open //./pipe/docker_engine: Le fichier spécifié est introuvable.`
		if strings.Contains(err.Error(), `open //./pipe/docker_engine`) {
			// Checks if client is running with elevated privileges
			if f, elevatedErr := os.Open(`\\.\PHYSICALDRIVE0`); elevatedErr != nil {
				err = errors.Wrap(err, "in the default daemon configuration on Windows, the docker client must be run with elevated privileges to connect")
			} else {
				_ = f.Close()
				err = errors.Wrap(err, "this error may indicate that the docker daemon is not running")
			}
		}

		return nil, errors.Wrap(err, "error during connect")
	}
	return resp, nil
}

func (cli *Client) checkResponseErr(serverResp *http.Response) error {
	if serverResp == nil {
		return nil
	}
	if serverResp.StatusCode >= 200 && serverResp.StatusCode < 400 {
		return nil
	}

	var body []byte
	var err error
	var reqURL string
	if serverResp.Request != nil {
		reqURL = serverResp.Request.URL.String()
	}
	statusMsg := serverResp.Status
	if statusMsg == "" {
		statusMsg = http.StatusText(serverResp.StatusCode)
	}
	if serverResp.Body != nil {
		bodyMax := 1 * 1024 * 1024 // 1 MiB
		bodyR := &io.LimitedReader{
			R: serverResp.Body,
			N: int64(bodyMax),
		}
		body, err = io.ReadAll(bodyR)
		if err != nil {
			return err
		}
		if bodyR.N == 0 {
			return fmt.Errorf("request returned %s with a message (> %d bytes) for API route and version %s, check if the server supports the requested API version", statusMsg, bodyMax, reqURL)
		}
	}
	if len(body) == 0 {
		return fmt.Errorf("request returned %s for API route and version %s, check if the server supports the requested API version", statusMsg, reqURL)
	}

	var daemonErr error
	if serverResp.Header.Get("Content-Type") == "application/json" {
		var errorResponse struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &errorResponse); err != nil {
			return errors.Wrap(err, "Error reading JSON")
		}
		daemonErr = errors.New(strings.TrimSpace(errorResponse.Message))
	} else {
		daemonErr = errors.New(strings.TrimSpace(string(body)))
	}
	return errors.Wrap(daemonErr, "Error response from daemon")
}

func (cli *Client) addHeaders(req *http.Request, headers http.Header) *http.Request {
	for k, v := range headers {
		req.Header[http.CanonicalHeaderKey(k)] = v
	}
	return req
}

func ensureReaderClosed(response *http.Response) {
	if response.Body != nil {
		// Drain up to 512 bytes and close the body to let the Transport reuse the connection
		_, _ = io.CopyN(io.Discard, response.Body, 512)
		_ = response.Body.Close()
	}
}
