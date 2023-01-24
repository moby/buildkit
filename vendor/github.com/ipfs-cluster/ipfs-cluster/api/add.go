package api

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"

	cid "github.com/ipfs/go-cid"
	peer "github.com/libp2p/go-libp2p/core/peer"
)

// DefaultShardSize is the shard size for params objects created with DefaultParams().
var DefaultShardSize = uint64(100 * 1024 * 1024) // 100 MB

// AddedOutput carries information for displaying the standard ipfs output
// indicating a node of a file has been added.
type AddedOutput struct {
	Name        string    `json:"name" codec:"n,omitempty"`
	Cid         Cid       `json:"cid" codec:"c"`
	Bytes       uint64    `json:"bytes,omitempty" codec:"b,omitempty"`
	Size        uint64    `json:"size,omitempty" codec:"s,omitempty"`
	Allocations []peer.ID `json:"allocations,omitempty" codec:"a,omitempty"`
}

// IPFSAddParams groups options specific to the ipfs-adder, which builds
// UnixFS dags with the input files. This struct is embedded in AddParams.
type IPFSAddParams struct {
	Layout     string
	Chunker    string
	RawLeaves  bool
	Progress   bool
	CidVersion int
	HashFun    string
	NoCopy     bool
}

// AddParams contains all of the configurable parameters needed to specify the
// importing process of a file being added to an ipfs-cluster
type AddParams struct {
	PinOptions

	Local          bool
	Recursive      bool
	Hidden         bool
	Wrap           bool
	Shard          bool
	StreamChannels bool
	Format         string // selects with adder
	NoPin          bool

	IPFSAddParams
}

// DefaultAddParams returns a AddParams object with standard defaults
func DefaultAddParams() AddParams {
	return AddParams{
		Local:     false,
		Recursive: false,

		Hidden: false,
		Wrap:   false,
		Shard:  false,

		StreamChannels: true,

		Format: "unixfs",
		NoPin:  false,
		PinOptions: PinOptions{
			ReplicationFactorMin: 0,
			ReplicationFactorMax: 0,
			Name:                 "",
			Mode:                 PinModeRecursive,
			ShardSize:            DefaultShardSize,
			Metadata:             make(map[string]string),
			Origins:              nil,
		},
		IPFSAddParams: IPFSAddParams{
			Layout:     "", // corresponds to balanced layout
			Chunker:    "size-262144",
			RawLeaves:  false,
			Progress:   false,
			CidVersion: 0,
			HashFun:    "sha2-256",
			NoCopy:     false,
		},
	}
}

func parseBoolParam(q url.Values, name string, dest *bool) error {
	if v := q.Get(name); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("parameter %s invalid", name)
		}
		*dest = b
	}
	return nil
}

func parseIntParam(q url.Values, name string, dest *int) error {
	if v := q.Get(name); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("parameter %s invalid", name)
		}
		*dest = i
	}
	return nil
}

// AddParamsFromQuery parses the AddParams object from
// a URL.Query().
func AddParamsFromQuery(query url.Values) (AddParams, error) {
	params := DefaultAddParams()

	opts := &PinOptions{}
	err := opts.FromQuery(query)
	if err != nil {
		return params, err
	}
	params.PinOptions = *opts
	params.PinUpdate.Cid = cid.Undef // hardcode as does not make sense for adding

	layout := query.Get("layout")
	switch layout {
	case "trickle", "balanced", "":
		// nothing
	default:
		return params, errors.New("layout parameter is invalid")
	}
	params.Layout = layout

	chunker := query.Get("chunker")
	if chunker != "" {
		params.Chunker = chunker
	}

	hashF := query.Get("hash")
	if hashF != "" {
		params.HashFun = hashF
	}

	format := query.Get("format")
	switch format {
	case "car", "unixfs", "":
	default:
		return params, errors.New("format parameter is invalid")
	}
	params.Format = format

	err = parseBoolParam(query, "local", &params.Local)
	if err != nil {
		return params, err
	}

	err = parseBoolParam(query, "recursive", &params.Recursive)
	if err != nil {
		return params, err
	}

	err = parseBoolParam(query, "hidden", &params.Hidden)
	if err != nil {
		return params, err
	}
	err = parseBoolParam(query, "wrap-with-directory", &params.Wrap)
	if err != nil {
		return params, err
	}
	err = parseBoolParam(query, "shard", &params.Shard)
	if err != nil {
		return params, err
	}

	err = parseBoolParam(query, "progress", &params.Progress)
	if err != nil {
		return params, err
	}

	err = parseIntParam(query, "cid-version", &params.CidVersion)
	if err != nil {
		return params, err
	}

	// This mimics go-ipfs behavior.
	if params.CidVersion > 0 {
		params.RawLeaves = true
	}

	// If the raw-leaves param is empty, the default RawLeaves value will
	// take place (which may be true or false depending on
	// CidVersion). Otherwise, it will be explicitly set.
	err = parseBoolParam(query, "raw-leaves", &params.RawLeaves)
	if err != nil {
		return params, err
	}

	err = parseBoolParam(query, "stream-channels", &params.StreamChannels)
	if err != nil {
		return params, err
	}

	err = parseBoolParam(query, "nocopy", &params.NoCopy)
	if err != nil {
		return params, err
	}

	err = parseBoolParam(query, "no-pin", &params.NoPin)
	if err != nil {
		return params, err
	}

	return params, nil
}

// ToQueryString returns a url query string (key=value&key2=value2&...)
func (p AddParams) ToQueryString() (string, error) {
	pinOptsQuery, err := p.PinOptions.ToQuery()
	if err != nil {
		return "", err
	}
	query, err := url.ParseQuery(pinOptsQuery)
	if err != nil {
		return "", err
	}
	query.Set("shard", fmt.Sprintf("%t", p.Shard))
	query.Set("local", fmt.Sprintf("%t", p.Local))
	query.Set("recursive", fmt.Sprintf("%t", p.Recursive))
	query.Set("layout", p.Layout)
	query.Set("chunker", p.Chunker)
	query.Set("raw-leaves", fmt.Sprintf("%t", p.RawLeaves))
	query.Set("hidden", fmt.Sprintf("%t", p.Hidden))
	query.Set("wrap-with-directory", fmt.Sprintf("%t", p.Wrap))
	query.Set("progress", fmt.Sprintf("%t", p.Progress))
	query.Set("cid-version", fmt.Sprintf("%d", p.CidVersion))
	query.Set("hash", p.HashFun)
	query.Set("stream-channels", fmt.Sprintf("%t", p.StreamChannels))
	query.Set("nocopy", fmt.Sprintf("%t", p.NoCopy))
	query.Set("format", p.Format)
	query.Set("no-pin", fmt.Sprintf("%t", p.NoPin))
	return query.Encode(), nil
}

// Equals checks if p equals p2.
func (p AddParams) Equals(p2 AddParams) bool {
	return p.PinOptions.Equals(p2.PinOptions) &&
		p.Local == p2.Local &&
		p.Recursive == p2.Recursive &&
		p.Shard == p2.Shard &&
		p.Layout == p2.Layout &&
		p.Chunker == p2.Chunker &&
		p.RawLeaves == p2.RawLeaves &&
		p.Hidden == p2.Hidden &&
		p.Wrap == p2.Wrap &&
		p.CidVersion == p2.CidVersion &&
		p.HashFun == p2.HashFun &&
		p.StreamChannels == p2.StreamChannels &&
		p.NoCopy == p2.NoCopy &&
		p.Format == p2.Format &&
		p.NoPin == p2.NoPin
}
