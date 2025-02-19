package client

import (
	"context"
	"net/http"
	"path"

	"github.com/moby/buildkit/util/testutil/dockerd/client/types"
)

type PingResponse struct{}

func (cli *Client) Ping(ctx context.Context) (types.Ping, error) {
	var ping types.Ping

	// Using cli.buildRequest() + cli.doRequest() instead of cli.sendRequest()
	// because ping requests are used during API version negotiation, so we want
	// to hit the non-versioned /_ping endpoint, not /v1.xx/_ping
	req, err := cli.buildRequest(ctx, http.MethodHead, path.Join(cli.basePath, "/_ping"), nil, nil)
	if err != nil {
		return ping, err
	}
	serverResp, err := cli.doRequest(req)
	if err != nil {
		return types.Ping{}, err
	}
	defer ensureReaderClosed(serverResp)
	return parsePingResponse(cli, serverResp)
}

func parsePingResponse(cli *Client, resp *http.Response) (types.Ping, error) {
	var ping types.Ping
	err := cli.checkResponseErr(resp)
	return ping, err
}
