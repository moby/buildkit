package client

import (
	"context"
	"net/http"
	"path"
)

type PingResponse struct{}

func (cli *Client) Ping(ctx context.Context) error {
	// Using cli.buildRequest() + cli.doRequest() instead of cli.sendRequest()
	// because ping requests are used during API version negotiation, so we want
	// to hit the non-versioned /_ping endpoint, not /v1.xx/_ping
	req, err := cli.buildRequest(ctx, http.MethodHead, path.Join(cli.basePath, "/_ping"), nil, nil)
	if err != nil {
		return err
	}
	serverResp, err := cli.doRequest(req)
	if err != nil {
		return err
	}
	defer ensureReaderClosed(serverResp)
	return cli.checkResponseErr(serverResp)
}
