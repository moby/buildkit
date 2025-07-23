package resolvconf

import (
	"bytes"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRCOption(t *testing.T) {
	testcases := []struct {
		name     string
		options  string
		search   string
		expFound bool
		expValue string
	}{
		{
			name:    "Empty options",
			options: "",
			search:  "ndots",
		},
		{
			name:    "Not found",
			options: "ndots:0 edns0",
			search:  "trust-ad",
		},
		{
			name:     "Found with value",
			options:  "ndots:0 edns0",
			search:   "ndots",
			expFound: true,
			expValue: "0",
		},
		{
			name:     "Found without value",
			options:  "ndots:0 edns0",
			search:   "edns0",
			expFound: true,
			expValue: "",
		},
		{
			name:     "Found last value",
			options:  "ndots:0 edns0 ndots:1",
			search:   "ndots",
			expFound: true,
			expValue: "1",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			rc, err := Parse(bytes.NewBufferString("options "+tc.options), "")
			require.NoError(t, err)
			value, found := rc.Option(tc.search)
			assert.Equal(t, tc.expFound, found)
			assert.Equal(t, tc.expValue, value)
		})
	}
}

var (
	a2s = sliceutilMapper(netip.Addr.String)
	s2a = sliceutilMapper(netip.MustParseAddr)
)

// Test that a resolv.conf file can be modified using OverrideXXX() methods
// to modify nameservers/search/options directives, and tha options can be
// added via AddOption().
func TestRCModify(t *testing.T) {
	testcases := []struct {
		name            string
		inputNS         []string
		inputSearch     []string
		inputOptions    []string
		noOverrides     bool // Whether to apply overrides (empty lists are valid overrides).
		overrideNS      []string
		overrideSearch  []string
		overrideOptions []string
		addOption       string
		expContent      string
	}{
		{
			name:    "No content no overrides",
			inputNS: []string{},
			expContent: `
# Based on host file: ''
# Overrides: [nameservers search options]
`,
		},
		{
			name:         "No overrides",
			noOverrides:  true,
			inputNS:      []string{"1.2.3.4"},
			inputSearch:  []string{"invalid"},
			inputOptions: []string{"ndots:0"},
			expContent: `nameserver 1.2.3.4
search invalid
options ndots:0

# Based on host file: ''
# Overrides: []
# Option ndots from: host
`,
		},
		{
			name:         "Empty overrides",
			inputNS:      []string{"1.2.3.4"},
			inputSearch:  []string{"invalid"},
			inputOptions: []string{"ndots:0"},
			expContent: `
# Based on host file: ''
# Overrides: [nameservers search options]
`,
		},
		{
			name:            "Overrides",
			inputNS:         []string{"1.2.3.4"},
			inputSearch:     []string{"invalid"},
			inputOptions:    []string{"ndots:0"},
			overrideNS:      []string{"2.3.4.5", "fdba:acdd:587c::53"},
			overrideSearch:  []string{"com", "invalid", "example"},
			overrideOptions: []string{"ndots:1", "edns0", "trust-ad"},
			expContent: `nameserver 2.3.4.5
nameserver fdba:acdd:587c::53
search com invalid example
options ndots:1 edns0 trust-ad

# Based on host file: ''
# Overrides: [nameservers search options]
# Option ndots from: override
`,
		},
		{
			name:         "Add option no overrides",
			noOverrides:  true,
			inputNS:      []string{"1.2.3.4"},
			inputSearch:  []string{"invalid"},
			inputOptions: []string{"ndots:0"},
			addOption:    "attempts:3",
			expContent: `nameserver 1.2.3.4
search invalid
options ndots:0 attempts:3

# Based on host file: ''
# Overrides: []
# Option ndots from: host
`,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tc := tc
			var input string
			if len(tc.inputNS) != 0 {
				for _, ns := range tc.inputNS {
					input += "nameserver " + ns + "\n"
				}
			}
			if len(tc.inputSearch) != 0 {
				input += "search " + strings.Join(tc.inputSearch, " ") + "\n"
			}
			if len(tc.inputOptions) != 0 {
				input += "options " + strings.Join(tc.inputOptions, " ") + "\n"
			}
			rc, err := Parse(bytes.NewBufferString(input), "")
			require.NoError(t, err)
			assert.Equal(t, tc.inputNS, a2s(rc.NameServers()))
			assert.Equal(t, tc.inputSearch, rc.Search())
			assert.Equal(t, tc.inputOptions, rc.Options())

			if !tc.noOverrides {
				overrideNS := s2a(tc.overrideNS)
				rc.OverrideNameServers(overrideNS)
				rc.OverrideSearch(tc.overrideSearch)
				rc.OverrideOptions(tc.overrideOptions)

				assert.Equal(t, overrideNS, rc.NameServers())
				assert.Equal(t, tc.overrideSearch, rc.Search())
				assert.Equal(t, tc.overrideOptions, rc.Options())
			}

			if tc.addOption != "" {
				options := rc.Options()
				rc.AddOption(tc.addOption)
				assert.Equal(t, append(options, tc.addOption), rc.Options())
			}

			content, err := rc.Generate(true)
			require.NoError(t, err)
			assert.Equal(t, string(tc.expContent), string(content))
		})
	}
}

func TestRCTransformForLegacyNw(t *testing.T) {
	testcases := []struct {
		name       string
		input      string
		ipv6       bool
		overrideNS []string
		expContent string
	}{
		{
			name:  "Routable IPv4 only",
			input: "nameserver 10.0.0.1",
			expContent: `nameserver 10.0.0.1

# Based on host file: '/etc/resolv.conf' (legacy)
# Overrides: []
`,
		},
		{
			name:  "Routable IPv4 and IPv6, ipv6 enabled",
			input: "nameserver 10.0.0.1\nnameserver fdb6:b8fe:b528::1",
			ipv6:  true,
			expContent: `nameserver 10.0.0.1
nameserver fdb6:b8fe:b528::1

# Based on host file: '/etc/resolv.conf' (legacy)
# Overrides: []
`,
		},
		{
			name:  "Routable IPv4 and IPv6, ipv6 disabled",
			input: "nameserver 10.0.0.1\nnameserver fdb6:b8fe:b528::1",
			ipv6:  false,
			expContent: `nameserver 10.0.0.1

# Based on host file: '/etc/resolv.conf' (legacy)
# Overrides: []
`,
		},
		{
			name:  "IPv4 localhost, ipv6 disabled",
			input: "nameserver 127.0.0.53",
			ipv6:  false,
			expContent: `nameserver 8.8.8.8
nameserver 8.8.4.4

# Based on host file: '/etc/resolv.conf' (legacy)
# Used default nameservers.
# Overrides: []
`,
		},
		{
			name:  "IPv4 localhost, ipv6 enabled",
			input: "nameserver 127.0.0.53",
			ipv6:  true,
			expContent: `nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 2001:4860:4860::8888
nameserver 2001:4860:4860::8844

# Based on host file: '/etc/resolv.conf' (legacy)
# Used default nameservers.
# Overrides: []
`,
		},
		{
			name:  "IPv4 and IPv6 localhost, ipv6 disabled",
			input: "nameserver 127.0.0.53\nnameserver ::1",
			ipv6:  false,
			expContent: `nameserver 8.8.8.8
nameserver 8.8.4.4

# Based on host file: '/etc/resolv.conf' (legacy)
# Used default nameservers.
# Overrides: []
`,
		},
		{
			name:  "IPv4 and IPv6 localhost, ipv6 enabled",
			input: "nameserver 127.0.0.53\nnameserver ::1",
			ipv6:  true,
			expContent: `nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 2001:4860:4860::8888
nameserver 2001:4860:4860::8844

# Based on host file: '/etc/resolv.conf' (legacy)
# Used default nameservers.
# Overrides: []
`,
		},
		{
			name:  "IPv4 localhost, IPv6 routeable, ipv6 enabled",
			input: "nameserver 127.0.0.53\nnameserver fd3e:2d1a:1f5a::1",
			ipv6:  true,
			expContent: `nameserver fd3e:2d1a:1f5a::1

# Based on host file: '/etc/resolv.conf' (legacy)
# Overrides: []
`,
		},
		{
			name:  "IPv4 localhost, IPv6 routeable, ipv6 disabled",
			input: "nameserver 127.0.0.53\nnameserver fd3e:2d1a:1f5a::1",
			ipv6:  false,
			expContent: `nameserver 8.8.8.8
nameserver 8.8.4.4

# Based on host file: '/etc/resolv.conf' (legacy)
# Used default nameservers.
# Overrides: []
`,
		},
		{
			name:       "Override nameservers",
			input:      "nameserver 127.0.0.53",
			overrideNS: []string{"127.0.0.1", "::1"},
			ipv6:       false,
			expContent: `nameserver 127.0.0.1
nameserver ::1

# Based on host file: '/etc/resolv.conf' (legacy)
# Overrides: [nameservers]
`,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tc := tc
			rc, err := Parse(bytes.NewBufferString(tc.input), "/etc/resolv.conf")
			require.NoError(t, err)
			if tc.overrideNS != nil {
				rc.OverrideNameServers(s2a(tc.overrideNS))
			}

			rc.TransformForLegacyNw(tc.ipv6)

			content, err := rc.Generate(true)
			require.NoError(t, err)
			assert.Equal(t, string(tc.expContent), string(content))
		})
	}
}

func TestRCTransformForIntNS(t *testing.T) {
	mke := func(addr string, hostLoopback bool) ExtDNSEntry {
		return ExtDNSEntry{
			Addr:         netip.MustParseAddr(addr),
			HostLoopback: hostLoopback,
		}
	}

	testcases := []struct {
		name            string
		input           string
		intNameServer   string
		overrideNS      []string
		overrideOptions []string
		reqdOptions     []string
		expContent      string
		expExtServers   []ExtDNSEntry
		expErr          string
	}{
		{
			name:          "IPv4 only",
			input:         "nameserver 10.0.0.1",
			expExtServers: []ExtDNSEntry{mke("10.0.0.1", true)},
			expContent: `nameserver 127.0.0.11

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(10.0.0.1)]
# Overrides: []
`,
		},
		{
			name:  "IPv4 and IPv6, ipv6 enabled",
			input: "nameserver 10.0.0.1\nnameserver fdb6:b8fe:b528::1",
			expExtServers: []ExtDNSEntry{
				mke("10.0.0.1", true),
				mke("fdb6:b8fe:b528::1", true),
			},
			expContent: `nameserver 127.0.0.11

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(10.0.0.1) host(fdb6:b8fe:b528::1)]
# Overrides: []
`,
		},
		{
			name:          "IPv4 localhost",
			input:         "nameserver 127.0.0.53",
			expExtServers: []ExtDNSEntry{mke("127.0.0.53", true)},
			expContent: `nameserver 127.0.0.11

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(127.0.0.53)]
# Overrides: []
`,
		},
		{
			// Overriding the nameserver with a localhost address means use the container's
			// loopback interface, not the host's.
			name:          "IPv4 localhost override",
			input:         "nameserver 10.0.0.1",
			overrideNS:    []string{"127.0.0.53"},
			expExtServers: []ExtDNSEntry{mke("127.0.0.53", false)},
			expContent: `nameserver 127.0.0.11

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [127.0.0.53]
# Overrides: [nameservers]
`,
		},
		{
			name:          "IPv6 only",
			input:         "nameserver fd14:6e0e:f855::1",
			expExtServers: []ExtDNSEntry{mke("fd14:6e0e:f855::1", true)},
			expContent: `nameserver 127.0.0.11

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(fd14:6e0e:f855::1)]
# Overrides: []
`,
		},
		{
			name:  "IPv4 and IPv6 localhost",
			input: "nameserver 127.0.0.53\nnameserver ::1",
			expExtServers: []ExtDNSEntry{
				mke("127.0.0.53", true),
				mke("::1", true),
			},
			expContent: `nameserver 127.0.0.11

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(127.0.0.53) host(::1)]
# Overrides: []
`,
		},
		{
			name:          "ndots present and required",
			input:         "nameserver 127.0.0.53\noptions ndots:1",
			reqdOptions:   []string{"ndots:0"},
			expExtServers: []ExtDNSEntry{mke("127.0.0.53", true)},
			expContent: `nameserver 127.0.0.11
options ndots:1

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(127.0.0.53)]
# Overrides: []
# Option ndots from: host
`,
		},
		{
			name:          "ndots missing but required",
			input:         "nameserver 127.0.0.53",
			reqdOptions:   []string{"ndots:0"},
			expExtServers: []ExtDNSEntry{mke("127.0.0.53", true)},
			expContent: `nameserver 127.0.0.11
options ndots:0

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(127.0.0.53)]
# Overrides: []
# Option ndots from: internal
`,
		},
		{
			name:            "ndots host, override and required",
			input:           "nameserver 127.0.0.53",
			reqdOptions:     []string{"ndots:0"},
			overrideOptions: []string{"ndots:2"},
			expExtServers:   []ExtDNSEntry{mke("127.0.0.53", true)},
			expContent: `nameserver 127.0.0.11
options ndots:2

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(127.0.0.53)]
# Overrides: [options]
# Option ndots from: override
`,
		},
		{
			name:          "Extra required options",
			input:         "nameserver 127.0.0.53\noptions trust-ad",
			reqdOptions:   []string{"ndots:0", "attempts:3", "edns0", "trust-ad"},
			expExtServers: []ExtDNSEntry{mke("127.0.0.53", true)},
			expContent: `nameserver 127.0.0.11
options trust-ad ndots:0 attempts:3 edns0

# Based on host file: '/etc/resolv.conf' (internal resolver)
# ExtServers: [host(127.0.0.53)]
# Overrides: []
# Option ndots from: internal
`,
		},
		{
			name:  "No config",
			input: "",
			expContent: `nameserver 127.0.0.11

# Based on host file: '/etc/resolv.conf' (internal resolver)
# NO EXTERNAL NAMESERVERS DEFINED
# Overrides: []
`,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tc := tc
			rc, err := Parse(bytes.NewBufferString(tc.input), "/etc/resolv.conf")
			require.NoError(t, err)

			if tc.intNameServer == "" {
				tc.intNameServer = "127.0.0.11"
			}
			if len(tc.overrideNS) > 0 {
				rc.OverrideNameServers(s2a(tc.overrideNS))
			}
			if len(tc.overrideOptions) > 0 {
				rc.OverrideOptions(tc.overrideOptions)
			}
			intNS := netip.MustParseAddr(tc.intNameServer)
			extNameServers, err := rc.TransformForIntNS(intNS, tc.reqdOptions)
			if tc.expErr != "" {
				assert.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)

			content, err := rc.Generate(true)
			require.NoError(t, err)
			assert.Equal(t, string(tc.expContent), string(content))
			assert.Equal(t, tc.expExtServers, extNameServers)
		})
	}
}

// Check that invalid ndots options in the host's file are ignored, unless
// starting the internal resolver (which requires an ndots option), in which
// case invalid ndots should be replaced.
func TestRCTransformForIntNSInvalidNdots(t *testing.T) {
	testcases := []struct {
		name         string
		options      string
		reqdOptions  []string
		expVal       string
		expOptions   []string
		expNDotsFrom string
	}{
		{
			name:         "Negative value",
			options:      "options ndots:-1",
			expOptions:   []string{"ndots:-1"},
			expVal:       "-1",
			expNDotsFrom: "host",
		},
		{
			name:         "Invalid values with reqd ndots",
			options:      "options ndots:-1 foo:bar ndots ndots:",
			reqdOptions:  []string{"ndots:2"},
			expVal:       "2",
			expNDotsFrom: "internal",
			expOptions:   []string{"foo:bar", "ndots:2"},
		},
		{
			name:         "Valid value with reqd ndots",
			options:      "options ndots:1 foo:bar ndots ndots:",
			reqdOptions:  []string{"ndots:2"},
			expVal:       "1",
			expNDotsFrom: "host",
			expOptions:   []string{"ndots:1", "foo:bar"},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			content := "nameserver 8.8.8.8\n" + tc.options
			rc, err := Parse(bytes.NewBufferString(content), "/etc/resolv.conf")
			require.NoError(t, err)
			_, err = rc.TransformForIntNS(netip.MustParseAddr("127.0.0.11"), tc.reqdOptions)
			require.NoError(t, err)

			val, found := rc.Option("ndots")
			assert.True(t, found)
			assert.Equal(t, tc.expVal, val)
			assert.Equal(t, tc.expNDotsFrom, rc.md.NDotsFrom)
			assert.Equal(t, tc.expOptions, rc.options)
		})
	}
}

func TestRCRead(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "resolv.conf")

	// Try to read a nonexistent file, equivalent to an empty file.
	_, err := Load(path)
	require.ErrorIs(t, err, os.ErrNotExist)

	err = os.WriteFile(path, []byte("options edns0"), 0o644)
	require.NoError(t, err)

	// Read that file in the constructor.
	rc, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"edns0"}, rc.Options())

	// Pass in an os.File, check the path is extracted.
	file, err := os.Open(path)
	require.NoError(t, err)
	rc, err = Parse(file, "")
	_ = file.Close()
	require.NoError(t, err)
	assert.Equal(t, path, rc.md.SourcePath)
}

func TestRCInvalidNS(t *testing.T) {
	// A resolv.conf with an invalid nameserver address.
	rc, err := Parse(bytes.NewBufferString("nameserver 1.2.3.4.5"), "")
	require.NoError(t, err)

	content, err := rc.Generate(true)
	require.NoError(t, err)
	assert.Equal(t, `
# Based on host file: ''
# Invalid nameservers: [1.2.3.4.5]
# Overrides: []
`, string(content))
}

func TestRCSetHeader(t *testing.T) {
	rc, err := Parse(bytes.NewBufferString("nameserver 127.0.0.53"), "/etc/resolv.conf")
	require.NoError(t, err)

	rc.SetHeader("# This is a comment.")

	content, err := rc.Generate(true)
	require.NoError(t, err)
	assert.Equal(t, `# This is a comment.

nameserver 127.0.0.53

# Based on host file: '/etc/resolv.conf'
# Overrides: []
`, string(content))
}

func TestRCUnknownDirectives(t *testing.T) {
	const input = `
something unexpected
nameserver 127.0.0.53
options ndots:1
unrecognised thing
`
	rc, err := Parse(bytes.NewBufferString(input), "/etc/resolv.conf")
	require.NoError(t, err)

	content, err := rc.Generate(true)
	require.NoError(t, err)
	assert.Equal(t, `nameserver 127.0.0.53
options ndots:1
something unexpected
unrecognised thing

# Based on host file: '/etc/resolv.conf'
# Overrides: []
# Option ndots from: host
`, string(content))
}

func BenchmarkGenerate(b *testing.B) {
	rc := &ResolvConf{
		nameServers: []netip.Addr{
			netip.MustParseAddr("8.8.8.8"),
			netip.MustParseAddr("1.1.1.1"),
		},
		search:  []string{"example.com", "svc.local"},
		options: []string{"ndots:1", "ndots:2", "ndots:3"},
		other:   []string{"something", "something else", "something else"},
		md: metadata{
			Header: `# Generated by Docker Engine.
# This file can be edited; Docker Engine will not make further changes once it
# has been modified.`,
			NSOverride:      true,
			SearchOverride:  true,
			OptionsOverride: true,
			NDotsFrom:       "host",
			ExtNameServers: []ExtDNSEntry{
				{Addr: netip.MustParseAddr("127.0.0.53"), HostLoopback: true},
				{Addr: netip.MustParseAddr("2.2.2.2"), HostLoopback: false},
			},
			InvalidNSs: []string{"256.256.256.256"},
			Warnings:   []string{"bad nameserver ignored"},
		},
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := rc.Generate(true)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func sliceutilMapper[In, Out any](fn func(In) Out) func([]In) []Out {
	return func(s []In) []Out {
		res := make([]Out, len(s))
		for i, v := range s {
			res[i] = fn(v)
		}
		return res
	}
}
