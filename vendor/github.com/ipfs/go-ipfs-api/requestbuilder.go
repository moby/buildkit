package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// RequestBuilder is an IPFS commands request builder.
type RequestBuilder struct {
	command string
	args    []string
	opts    map[string]string
	headers map[string]string
	body    io.Reader

	shell *Shell
}

// Arguments adds the arguments to the args.
func (r *RequestBuilder) Arguments(args ...string) *RequestBuilder {
	r.args = append(r.args, args...)
	return r
}

// BodyString sets the request body to the given string.
func (r *RequestBuilder) BodyString(body string) *RequestBuilder {
	return r.Body(strings.NewReader(body))
}

// BodyBytes sets the request body to the given buffer.
func (r *RequestBuilder) BodyBytes(body []byte) *RequestBuilder {
	return r.Body(bytes.NewReader(body))
}

// Body sets the request body to the given reader.
func (r *RequestBuilder) Body(body io.Reader) *RequestBuilder {
	r.body = body
	return r
}

// Option sets the given option.
func (r *RequestBuilder) Option(key string, value interface{}) *RequestBuilder {
	var s string
	switch v := value.(type) {
	case bool:
		s = strconv.FormatBool(v)
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		// slow case.
		s = fmt.Sprint(value)
	}
	if r.opts == nil {
		r.opts = make(map[string]string, 1)
	}
	r.opts[key] = s
	return r
}

// Header sets the given header.
func (r *RequestBuilder) Header(name, value string) *RequestBuilder {
	if r.headers == nil {
		r.headers = make(map[string]string, 1)
	}
	r.headers[name] = value
	return r
}

// Send sends the request and return the response.
func (r *RequestBuilder) Send(ctx context.Context) (*Response, error) {
	req := NewRequest(ctx, r.shell.url, r.command, r.args...)
	req.Opts = r.opts
	req.Headers = r.headers
	req.Body = r.body
	return req.Send(&r.shell.httpcli)
}

// Exec sends the request a request and decodes the response.
func (r *RequestBuilder) Exec(ctx context.Context, res interface{}) error {
	httpRes, err := r.Send(ctx)
	if err != nil {
		return err
	}

	if res == nil {
		lateErr := httpRes.Close()
		if httpRes.Error != nil {
			return httpRes.Error
		}
		return lateErr
	}

	return httpRes.Decode(res)
}
