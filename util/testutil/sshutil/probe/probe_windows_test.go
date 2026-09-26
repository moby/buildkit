package probe

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestCheckIdentity(t *testing.T) {
	key, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	public, err := ssh.NewPublicKey(key)
	require.NoError(t, err)
	blob := public.Marshal()
	expected := base64.StdEncoding.EncodeToString(blob)
	keys := []*agent.Key{{Format: public.Type(), Blob: blob}}
	require.NoError(t, CheckIdentity(keys, expected))
	for _, tc := range []struct {
		name string
		keys []*agent.Key
		want string
	}{
		{"empty", nil, expected},
		{"extra", append(keys, keys[0]), expected},
		{"wrong", []*agent.Key{{Blob: []byte("different")}}, expected},
		{"invalid-base64", keys, "!"},
		{"invalid-key", keys, base64.StdEncoding.EncodeToString([]byte("invalid"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, CheckIdentity(tc.keys, tc.want))
		})
	}
}

func TestCheckRejection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response []byte
		wantErr  string
	}{
		{"failure", []byte{0, 0, 0, 1, 5}, ""},
		{"success", []byte{0, 0, 0, 1, 6}, "response type 6"},
		{"identities-answer", []byte{0, 0, 0, 5, 12, 0, 0, 0, 0}, "response length 5"},
		{"wrong-type", []byte{0, 0, 0, 1, 12}, "response type 12"},
		{"failure-with-payload", []byte{0, 0, 0, 2, 5, 0}, "response length 2"},
		{"empty", []byte{0, 0, 0, 0}, "response length 0"},
		{"oversized", []byte{255, 255, 255, 255}, "response length 4294967295"},
		{"closed", nil, "reading SSH mutation response length"},
		{"truncated-header", []byte{0, 0}, "reading SSH mutation response length"},
		{"truncated-payload", []byte{0, 0, 0, 1}, "reading SSH mutation response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var written bytes.Buffer
			conn := struct {
				io.Reader
				io.Writer
			}{bytes.NewReader(tc.response), &written}
			request := []byte{19}
			err := checkRejection(conn, request)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
			require.Equal(t, append(binary.BigEndian.AppendUint32(nil, 1), request...), written.Bytes())
		})
	}
}

func TestDefaults(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	require.Error(t, checkDefaults(Report{Endpoint: "not the default", AuthSock: "unexpected"}))
}

func TestMutationIdentitiesAnswer(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	add := ssh.Marshal(struct {
		Type    string `sshtype:"17"`
		Public  []byte
		Private []byte
		Comment string
	}{Type: ssh.KeyAlgoED25519, Public: public, Private: key})
	for _, tc := range []struct {
		name    string
		request []byte
	}{
		{"add", add},
		{"remove-all", []byte{19}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			require.NoError(t, client.SetDeadline(time.Now().Add(5*time.Second)))
			require.NoError(t, server.SetDeadline(time.Now().Add(5*time.Second)))
			received := make(chan []byte, 1)
			go func() {
				defer server.Close()
				request := make([]byte, 4+len(tc.request))
				if _, err := io.ReadFull(server, request); err != nil {
					received <- nil
					return
				}
				received <- request
				// A valid identities answer is not a mutation rejection.
				_, _ = server.Write([]byte{0, 0, 0, 5, 12, 0, 0, 0, 0})
			}()
			require.ErrorContains(t, checkRejection(client, tc.request), "response length 5")
			require.Equal(t, append(binary.BigEndian.AppendUint32(nil, uint32(len(tc.request))), tc.request...), <-received)
		})
	}
}
