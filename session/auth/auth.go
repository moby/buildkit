package auth

import (
	"context"
	"io/ioutil"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/moby/buildkit/session"
	netcontext "golang.org/x/net/context"
	"google.golang.org/grpc"
)

func NewDockerAuthProvider() session.Attachable {
	return &authProvider{
		config: config.LoadDefaultConfigFile(ioutil.Discard),
	}
}

type authProvider struct {
	config *configfile.ConfigFile
}

func (ap *authProvider) Register(server *grpc.Server) {
	RegisterAuthServer(server, ap)
}

func (ap *authProvider) Credentials(ctx netcontext.Context, req *CredentialsRequest) (*CredentialsResponse, error) {
	if req.Host == "registry-1.docker.io" {
		req.Host = "https://index.docker.io/v1/"
	}
	ac, err := ap.config.GetAuthConfig(req.Host)
	if err != nil {
		return nil, err
	}
	res := &CredentialsResponse{}
	if ac.IdentityToken != "" {
		res.Secret = ac.IdentityToken
	} else {
		res.Username = ac.Username
		res.Secret = ac.Password
	}
	return res, nil
}

func CredentialsFunc(ctx context.Context, c session.Caller) func(string) (string, string, error) {
	return func(host string) (string, string, error) {
		client := NewAuthClient(c.Conn())

		resp, err := client.Credentials(ctx, &CredentialsRequest{
			Host: host,
		})
		if err != nil {
			return "", "", err
		}
		return resp.Username, resp.Secret, nil
	}
}
