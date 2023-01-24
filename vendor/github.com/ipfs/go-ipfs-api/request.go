package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"

	files "github.com/ipfs/go-ipfs-files"
)

type Request struct {
	Ctx     context.Context
	ApiBase string
	Command string
	Args    []string
	Opts    map[string]string
	Body    io.Reader
	Headers map[string]string
}

func NewRequest(ctx context.Context, url, command string, args ...string) *Request {
	if !strings.HasPrefix(url, "http") {
		url = "http://" + url
	}

	opts := map[string]string{
		"encoding":        "json",
		"stream-channels": "true",
	}
	return &Request{
		Ctx:     ctx,
		ApiBase: url + "/api/v0",
		Command: command,
		Args:    args,
		Opts:    opts,
		Headers: make(map[string]string),
	}
}

type trailerReader struct {
	resp *http.Response
}

func (r *trailerReader) Read(b []byte) (int, error) {
	n, err := r.resp.Body.Read(b)
	if err != nil {
		if e := r.resp.Trailer.Get("X-Stream-Error"); e != "" {
			err = errors.New(e)
		}
	}
	return n, err
}

func (r *trailerReader) Close() error {
	return r.resp.Body.Close()
}

type Response struct {
	Output io.ReadCloser
	Error  *Error
}

func (r *Response) Close() error {
	if r.Output != nil {
		// always drain output (response body)
		_, err1 := io.Copy(ioutil.Discard, r.Output)
		err2 := r.Output.Close()
		if err1 != nil {
			return err1
		}
		if err2 != nil {
			return err2
		}
	}
	return nil
}

func (r *Response) Decode(dec interface{}) error {
	defer r.Close()
	if r.Error != nil {
		return r.Error
	}

	return json.NewDecoder(r.Output).Decode(dec)
}

type Error struct {
	Command string
	Message string
	Code    int
}

func (e *Error) Error() string {
	var out string
	if e.Command != "" {
		out = e.Command + ": "
	}
	if e.Code != 0 {
		out = fmt.Sprintf("%s%d: ", out, e.Code)
	}
	return out + e.Message
}

func (r *Request) Send(c *http.Client) (*Response, error) {
	url := r.getURL()
	req, err := http.NewRequest("POST", url, r.Body)
	if err != nil {
		return nil, err
	}

	req = req.WithContext(r.Ctx)

	// Add any headers that were supplied via the RequestBuilder.
	for k, v := range r.Headers {
		req.Header.Add(k, v)
	}

	if fr, ok := r.Body.(*files.MultiFileReader); ok {
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+fr.Boundary())
		req.Header.Set("Content-Disposition", "form-data; name=\"files\"")
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}

	contentType := resp.Header.Get("Content-Type")
	parts := strings.Split(contentType, ";")
	contentType = parts[0]

	nresp := new(Response)

	nresp.Output = &trailerReader{resp}
	if resp.StatusCode >= http.StatusBadRequest {
		e := &Error{
			Command: r.Command,
		}
		switch {
		case resp.StatusCode == http.StatusNotFound:
			e.Message = "command not found"
		case contentType == "text/plain":
			out, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ipfs-shell: warning! response (%d) read error: %s\n", resp.StatusCode, err)
			}
			e.Message = string(out)
		case contentType == "application/json":
			if err = json.NewDecoder(resp.Body).Decode(e); err != nil {
				fmt.Fprintf(os.Stderr, "ipfs-shell: warning! response (%d) unmarshall error: %s\n", resp.StatusCode, err)
			}
		default:
			fmt.Fprintf(os.Stderr, "ipfs-shell: warning! unhandled response (%d) encoding: %s", resp.StatusCode, contentType)
			out, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ipfs-shell: response (%d) read error: %s\n", resp.StatusCode, err)
			}
			e.Message = fmt.Sprintf("unknown ipfs-shell error encoding: %q - %q", contentType, out)
		}
		nresp.Error = e
		nresp.Output = nil

		// drain body and close
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}

	return nresp, nil
}

func (r *Request) getURL() string {

	values := make(url.Values)
	for _, arg := range r.Args {
		values.Add("arg", arg)
	}
	for k, v := range r.Opts {
		values.Add(k, v)
	}

	return fmt.Sprintf("%s/%s?%s", r.ApiBase, r.Command, values.Encode())
}
