package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ipfs-cluster/ipfs-cluster/api"
	"go.uber.org/multierr"

	"go.opencensus.io/trace"
)

type responseDecoder func(d *json.Decoder) error

func (c *defaultClient) do(
	ctx context.Context,
	method, path string,
	headers map[string]string,
	body io.Reader,
	obj interface{},
) error {

	resp, err := c.doRequest(ctx, method, path, headers, body)
	if err != nil {
		return api.Error{Code: 0, Message: err.Error()}
	}
	return c.handleResponse(resp, obj)
}

func (c *defaultClient) doStream(
	ctx context.Context,
	method, path string,
	headers map[string]string,
	body io.Reader,
	outHandler responseDecoder,
) error {

	resp, err := c.doRequest(ctx, method, path, headers, body)
	if err != nil {
		return api.Error{Code: 0, Message: err.Error()}
	}
	return c.handleStreamResponse(resp, outHandler)
}

func (c *defaultClient) doRequest(
	ctx context.Context,
	method, path string,
	headers map[string]string,
	body io.Reader,
) (*http.Response, error) {
	span := trace.FromContext(ctx)
	span.AddAttributes(
		trace.StringAttribute("method", method),
		trace.StringAttribute("path", path),
	)
	defer span.End()

	urlpath := c.net + "://" + c.hostname + "/" + strings.TrimPrefix(path, "/")
	logger.Debugf("%s: %s", method, urlpath)

	r, err := http.NewRequestWithContext(ctx, method, urlpath, body)
	if err != nil {
		return nil, err
	}
	if c.config.DisableKeepAlives {
		r.Close = true
	}

	if c.config.Username != "" {
		r.SetBasicAuth(c.config.Username, c.config.Password)
	}

	for k, v := range headers {
		r.Header.Set(k, v)
	}

	if body != nil {
		r.ContentLength = -1 // this lets go use "chunked".
	}

	r = r.WithContext(ctx)

	return c.client.Do(r)
}
func (c *defaultClient) handleResponse(resp *http.Response, obj interface{}) error {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()

	if err != nil {
		return api.Error{Code: resp.StatusCode, Message: err.Error()}
	}
	logger.Debugf("Response body: %s", body)

	switch {
	case resp.StatusCode == http.StatusAccepted:
		logger.Debug("Request accepted")
	case resp.StatusCode == http.StatusNoContent:
		logger.Debug("Request succeeded. Response has no content")
	default:
		if resp.StatusCode > 399 && resp.StatusCode < 600 {
			var apiErr api.Error
			err = json.Unmarshal(body, &apiErr)
			if err != nil {
				// not json. 404s etc.
				return api.Error{
					Code:    resp.StatusCode,
					Message: string(body),
				}
			}
			return apiErr
		}
		err = json.Unmarshal(body, obj)
		if err != nil {
			return api.Error{
				Code:    resp.StatusCode,
				Message: err.Error(),
			}
		}
	}
	return nil
}

func (c *defaultClient) handleStreamResponse(resp *http.Response, handler responseDecoder) error {
	if resp.StatusCode > 399 && resp.StatusCode < 600 {
		return c.handleResponse(resp, nil)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return api.Error{
			Code:    resp.StatusCode,
			Message: "expected streaming response with code 200/204",
		}
	}

	dec := json.NewDecoder(resp.Body)
	for {
		err := handler(dec)
		if err == io.EOF {
			// we need to check trailers
			break
		}
		if err != nil {
			logger.Error(err)
			return err
		}
	}

	trailerErrs := resp.Trailer.Values("X-Stream-Error")
	var err error
	for _, trailerErr := range trailerErrs {
		if trailerErr != "" {
			err = multierr.Append(err, errors.New(trailerErr))
		}
	}

	if err != nil {
		return api.Error{
			Code:    500,
			Message: err.Error(),
		}
	}
	return nil
}
