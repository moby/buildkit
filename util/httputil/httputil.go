package httputil

import (
	"crypto/tls"
	"net/http"
)

func SkipTLSClient(insecure bool) *http.Client {
	if !insecure {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}
