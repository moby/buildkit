package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipfs-cluster/ipfs-cluster/api"

	files "github.com/ipfs/go-ipfs-files"
	gopath "github.com/ipfs/go-path"
	peer "github.com/libp2p/go-libp2p/core/peer"

	"go.opencensus.io/trace"
)

// ID returns information about the cluster Peer.
func (c *defaultClient) ID(ctx context.Context) (api.ID, error) {
	ctx, span := trace.StartSpan(ctx, "client/ID")
	defer span.End()

	var id api.ID
	err := c.do(ctx, "GET", "/id", nil, nil, &id)
	return id, err
}

// Peers requests ID information for all cluster peers.
func (c *defaultClient) Peers(ctx context.Context, out chan<- api.ID) error {
	defer close(out)

	ctx, span := trace.StartSpan(ctx, "client/Peers")
	defer span.End()

	handler := func(dec *json.Decoder) error {
		var obj api.ID
		err := dec.Decode(&obj)
		if err != nil {
			return err
		}
		out <- obj
		return nil
	}

	return c.doStream(ctx, "GET", "/peers", nil, nil, handler)

}

type peerAddBody struct {
	PeerID string `json:"peer_id"`
}

// PeerAdd adds a new peer to the cluster.
func (c *defaultClient) PeerAdd(ctx context.Context, pid peer.ID) (api.ID, error) {
	ctx, span := trace.StartSpan(ctx, "client/PeerAdd")
	defer span.End()

	body := peerAddBody{pid.String()}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.Encode(body)

	var id api.ID
	err := c.do(ctx, "POST", "/peers", nil, &buf, &id)
	return id, err
}

// PeerRm removes a current peer from the cluster
func (c *defaultClient) PeerRm(ctx context.Context, id peer.ID) error {
	ctx, span := trace.StartSpan(ctx, "client/PeerRm")
	defer span.End()

	return c.do(ctx, "DELETE", fmt.Sprintf("/peers/%s", id.Pretty()), nil, nil, nil)
}

// Pin tracks a Cid with the given replication factor and a name for
// human-friendliness.
func (c *defaultClient) Pin(ctx context.Context, ci api.Cid, opts api.PinOptions) (api.Pin, error) {
	ctx, span := trace.StartSpan(ctx, "client/Pin")
	defer span.End()

	query, err := opts.ToQuery()
	if err != nil {
		return api.Pin{}, err
	}
	var pin api.Pin
	err = c.do(
		ctx,
		"POST",
		fmt.Sprintf(
			"/pins/%s?%s",
			ci.String(),
			query,
		),
		nil,
		nil,
		&pin,
	)
	return pin, err
}

// Unpin untracks a Cid from cluster.
func (c *defaultClient) Unpin(ctx context.Context, ci api.Cid) (api.Pin, error) {
	ctx, span := trace.StartSpan(ctx, "client/Unpin")
	defer span.End()
	var pin api.Pin
	err := c.do(ctx, "DELETE", fmt.Sprintf("/pins/%s", ci.String()), nil, nil, &pin)
	return pin, err
}

// PinPath allows to pin an element by the given IPFS path.
func (c *defaultClient) PinPath(ctx context.Context, path string, opts api.PinOptions) (api.Pin, error) {
	ctx, span := trace.StartSpan(ctx, "client/PinPath")
	defer span.End()

	var pin api.Pin
	ipfspath, err := gopath.ParsePath(path)
	if err != nil {
		return api.Pin{}, err
	}
	query, err := opts.ToQuery()
	if err != nil {
		return api.Pin{}, err
	}
	err = c.do(
		ctx,
		"POST",
		fmt.Sprintf(
			"/pins%s?%s",
			ipfspath.String(),
			query,
		),
		nil,
		nil,
		&pin,
	)

	return pin, err
}

// UnpinPath allows to unpin an item by providing its IPFS path.
// It returns the unpinned api.Pin information of the resolved Cid.
func (c *defaultClient) UnpinPath(ctx context.Context, p string) (api.Pin, error) {
	ctx, span := trace.StartSpan(ctx, "client/UnpinPath")
	defer span.End()

	var pin api.Pin
	ipfspath, err := gopath.ParsePath(p)
	if err != nil {
		return api.Pin{}, err
	}

	err = c.do(ctx, "DELETE", fmt.Sprintf("/pins%s", ipfspath.String()), nil, nil, &pin)
	return pin, err
}

// Allocations returns the consensus state listing all tracked items and
// the peers that should be pinning them.
func (c *defaultClient) Allocations(ctx context.Context, filter api.PinType, out chan<- api.Pin) error {
	defer close(out)

	ctx, span := trace.StartSpan(ctx, "client/Allocations")
	defer span.End()

	types := []api.PinType{
		api.DataType,
		api.MetaType,
		api.ClusterDAGType,
		api.ShardType,
	}

	var strFilter []string

	if filter == api.AllType {
		strFilter = []string{"all"}
	} else {
		for _, t := range types {
			if t&filter > 0 { // the filter includes this type
				strFilter = append(strFilter, t.String())
			}
		}
	}

	handler := func(dec *json.Decoder) error {
		var obj api.Pin
		err := dec.Decode(&obj)
		if err != nil {
			return err
		}
		out <- obj
		return nil
	}

	f := url.QueryEscape(strings.Join(strFilter, ","))
	return c.doStream(
		ctx,
		"GET",
		fmt.Sprintf("/allocations?filter=%s", f),
		nil,
		nil,
		handler)
}

// Allocation returns the current allocations for a given Cid.
func (c *defaultClient) Allocation(ctx context.Context, ci api.Cid) (api.Pin, error) {
	ctx, span := trace.StartSpan(ctx, "client/Allocation")
	defer span.End()

	var pin api.Pin
	err := c.do(ctx, "GET", fmt.Sprintf("/allocations/%s", ci.String()), nil, nil, &pin)
	return pin, err
}

// Status returns the current ipfs state for a given Cid. If local is true,
// the information affects only the current peer, otherwise the information
// is fetched from all cluster peers.
func (c *defaultClient) Status(ctx context.Context, ci api.Cid, local bool) (api.GlobalPinInfo, error) {
	ctx, span := trace.StartSpan(ctx, "client/Status")
	defer span.End()

	var gpi api.GlobalPinInfo
	err := c.do(
		ctx,
		"GET",
		fmt.Sprintf("/pins/%s?local=%t", ci.String(), local),
		nil,
		nil,
		&gpi,
	)
	return gpi, err
}

// StatusCids returns Status() information for the given Cids. If local is
// true, the information affects only the current peer, otherwise the
// information is fetched from all cluster peers.
func (c *defaultClient) StatusCids(ctx context.Context, cids []api.Cid, local bool, out chan<- api.GlobalPinInfo) error {
	return c.statusAllWithCids(ctx, api.TrackerStatusUndefined, cids, local, out)
}

// StatusAll gathers Status() for all tracked items. If a filter is
// provided, only entries matching the given filter statuses
// will be returned. A filter can be built by merging TrackerStatuses with
// a bitwise OR operation (st1 | st2 | ...). A "0" filter value (or
// api.TrackerStatusUndefined), means all.
func (c *defaultClient) StatusAll(ctx context.Context, filter api.TrackerStatus, local bool, out chan<- api.GlobalPinInfo) error {
	return c.statusAllWithCids(ctx, filter, nil, local, out)
}

func (c *defaultClient) statusAllWithCids(ctx context.Context, filter api.TrackerStatus, cids []api.Cid, local bool, out chan<- api.GlobalPinInfo) error {
	defer close(out)
	ctx, span := trace.StartSpan(ctx, "client/StatusAll")
	defer span.End()

	filterStr := ""
	if filter != api.TrackerStatusUndefined { // undefined filter means "all"
		filterStr = filter.String()
		if filterStr == "" {
			return errors.New("invalid filter value")
		}
	}

	cidsStr := make([]string, len(cids))
	for i, c := range cids {
		cidsStr[i] = c.String()
	}

	handler := func(dec *json.Decoder) error {
		var obj api.GlobalPinInfo
		err := dec.Decode(&obj)
		if err != nil {
			return err
		}
		out <- obj
		return nil
	}

	return c.doStream(
		ctx,
		"GET",
		fmt.Sprintf("/pins?local=%t&filter=%s&cids=%s",
			local, url.QueryEscape(filterStr), strings.Join(cidsStr, ",")),
		nil,
		nil,
		handler,
	)
}

// Recover retriggers pin or unpin ipfs operations for a Cid in error state.
// If local is true, the operation is limited to the current peer, otherwise
// it happens on every cluster peer.
func (c *defaultClient) Recover(ctx context.Context, ci api.Cid, local bool) (api.GlobalPinInfo, error) {
	ctx, span := trace.StartSpan(ctx, "client/Recover")
	defer span.End()

	var gpi api.GlobalPinInfo
	err := c.do(ctx, "POST", fmt.Sprintf("/pins/%s/recover?local=%t", ci.String(), local), nil, nil, &gpi)
	return gpi, err
}

// RecoverAll triggers Recover() operations on all tracked items. If local is
// true, the operation is limited to the current peer. Otherwise, it happens
// everywhere.
func (c *defaultClient) RecoverAll(ctx context.Context, local bool, out chan<- api.GlobalPinInfo) error {
	defer close(out)

	ctx, span := trace.StartSpan(ctx, "client/RecoverAll")
	defer span.End()

	handler := func(dec *json.Decoder) error {
		var obj api.GlobalPinInfo
		err := dec.Decode(&obj)
		if err != nil {
			return err
		}
		out <- obj
		return nil
	}

	return c.doStream(
		ctx,
		"POST",
		fmt.Sprintf("/pins/recover?local=%t", local),
		nil,
		nil,
		handler)
}

// Alerts returns information health events in the cluster (expired metrics
// etc.).
func (c *defaultClient) Alerts(ctx context.Context) ([]api.Alert, error) {
	ctx, span := trace.StartSpan(ctx, "client/Alert")
	defer span.End()

	var alerts []api.Alert
	err := c.do(ctx, "GET", "/health/alerts", nil, nil, &alerts)
	return alerts, err
}

// Version returns the ipfs-cluster peer's version.
func (c *defaultClient) Version(ctx context.Context) (api.Version, error) {
	ctx, span := trace.StartSpan(ctx, "client/Version")
	defer span.End()

	var ver api.Version
	err := c.do(ctx, "GET", "/version", nil, nil, &ver)
	return ver, err
}

// GetConnectGraph returns an ipfs-cluster connection graph.
// The serialized version, strings instead of pids, is returned
func (c *defaultClient) GetConnectGraph(ctx context.Context) (api.ConnectGraph, error) {
	ctx, span := trace.StartSpan(ctx, "client/GetConnectGraph")
	defer span.End()

	var graph api.ConnectGraph
	err := c.do(ctx, "GET", "/health/graph", nil, nil, &graph)
	return graph, err
}

// Metrics returns a map with the latest valid metrics of the given name
// for the current cluster peers.
func (c *defaultClient) Metrics(ctx context.Context, name string) ([]api.Metric, error) {
	ctx, span := trace.StartSpan(ctx, "client/Metrics")
	defer span.End()

	if name == "" {
		return nil, errors.New("bad metric name")
	}
	var metrics []api.Metric
	err := c.do(ctx, "GET", fmt.Sprintf("/monitor/metrics/%s", name), nil, nil, &metrics)
	return metrics, err
}

// MetricNames lists names of all metrics.
func (c *defaultClient) MetricNames(ctx context.Context) ([]string, error) {
	ctx, span := trace.StartSpan(ctx, "client/MetricNames")
	defer span.End()

	var metricsNames []string
	err := c.do(ctx, "GET", "/monitor/metrics", nil, nil, &metricsNames)
	return metricsNames, err
}

// RepoGC runs garbage collection on IPFS daemons of cluster peers and
// returns collected CIDs. If local is true, it would garbage collect
// only on contacted peer, otherwise on all peers' IPFS daemons.
func (c *defaultClient) RepoGC(ctx context.Context, local bool) (api.GlobalRepoGC, error) {
	ctx, span := trace.StartSpan(ctx, "client/RepoGC")
	defer span.End()

	var repoGC api.GlobalRepoGC
	err := c.do(
		ctx,
		"POST",
		fmt.Sprintf("/ipfs/gc?local=%t", local),
		nil,
		nil,
		&repoGC,
	)

	return repoGC, err
}

// WaitFor is a utility function that allows for a caller to wait until a CID
// status target is reached (as given in StatusFilterParams).
// It returns the final status for that CID and an error, if there was one.
//
// WaitFor works by calling Status() repeatedly and checking that returned
// peers have transitioned to the target TrackerStatus. It immediately returns
// an error when the an error is among the statuses (and an empty
// GlobalPinInfo).
//
// A special case exists for TrackerStatusPinned targets: in this case,
// TrackerStatusRemote statuses are ignored, so WaitFor will return when
// all Statuses are Pinned or Remote by default.
//
// The Limit parameter allows to specify finer-grained control to, for
// example, only wait until a number of peers reaches a status.
func WaitFor(ctx context.Context, c Client, fp StatusFilterParams) (api.GlobalPinInfo, error) {
	ctx, span := trace.StartSpan(ctx, "client/WaitFor")
	defer span.End()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sf := newStatusFilter()

	go sf.pollStatus(ctx, c, fp)
	go sf.filter(ctx, fp)

	var status api.GlobalPinInfo

	for {
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case err := <-sf.Err:
			return status, err
		case st, ok := <-sf.Out:
			if !ok { // channel closed
				return status, nil
			}
			status = st
		}
	}
}

// StatusFilterParams contains the parameters required
// to filter a stream of status results.
type StatusFilterParams struct {
	Cid       api.Cid
	Local     bool // query status from the local peer only
	Target    api.TrackerStatus
	Limit     int // wait for N peers reaching status. 0 == all
	CheckFreq time.Duration
}

type statusFilter struct {
	In, Out chan api.GlobalPinInfo
	Done    chan struct{}
	Err     chan error
}

func newStatusFilter() *statusFilter {
	return &statusFilter{
		In:   make(chan api.GlobalPinInfo),
		Out:  make(chan api.GlobalPinInfo),
		Done: make(chan struct{}),
		Err:  make(chan error),
	}
}

func (sf *statusFilter) filter(ctx context.Context, fp StatusFilterParams) {
	defer close(sf.Done)
	defer close(sf.Out)

	for {
		select {
		case <-ctx.Done():
			sf.Err <- ctx.Err()
			return
		case gblPinInfo, more := <-sf.In:
			if !more {
				return
			}
			ok, err := statusReached(fp.Target, gblPinInfo, fp.Limit)
			if err != nil {
				sf.Err <- err
				return
			}

			sf.Out <- gblPinInfo
			if !ok {
				continue
			}
			return
		}
	}
}

func (sf *statusFilter) pollStatus(ctx context.Context, c Client, fp StatusFilterParams) {
	ticker := time.NewTicker(fp.CheckFreq)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			sf.Err <- ctx.Err()
			return
		case <-ticker.C:
			gblPinInfo, err := c.Status(ctx, fp.Cid, fp.Local)
			if err != nil {
				sf.Err <- err
				return
			}
			logger.Debugf("pollStatus: status: %#v", gblPinInfo)
			sf.In <- gblPinInfo
		case <-sf.Done:
			close(sf.In)
			return
		}
	}
}

func statusReached(target api.TrackerStatus, gblPinInfo api.GlobalPinInfo, limit int) (bool, error) {
	// Specific case: return error if there are errors
	for _, pinInfo := range gblPinInfo.PeerMap {
		switch pinInfo.Status {
		case api.TrackerStatusUndefined,
			api.TrackerStatusClusterError,
			api.TrackerStatusPinError,
			api.TrackerStatusUnpinError:
			return false, fmt.Errorf("error has occurred while attempting to reach status: %s", target.String())
		}
	}

	// Specific case: when limit it set, just count how many targets we
	// reached.
	if limit > 0 {
		total := 0
		for _, pinInfo := range gblPinInfo.PeerMap {
			if pinInfo.Status == target {
				total++
			}
		}
		return total >= limit, nil
	}

	// General case: all statuses should be the target.
	// Specific case: when looking for Pinned, ignore status remote.
	for _, pinInfo := range gblPinInfo.PeerMap {
		if pinInfo.Status == api.TrackerStatusRemote && target == api.TrackerStatusPinned {
			continue
		}
		if pinInfo.Status == target {
			continue
		}
		return false, nil
	}

	// All statuses are the target, as otherwise we would have returned
	// false.
	return true, nil
}

// logic drawn from go-ipfs-cmds/cli/parse.go: appendFile
func makeSerialFile(fpath string, params api.AddParams) (string, files.Node, error) {
	if fpath == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		cwd, err = filepath.EvalSymlinks(cwd)
		if err != nil {
			return "", nil, err
		}
		fpath = cwd
	}

	fpath = filepath.ToSlash(filepath.Clean(fpath))

	stat, err := os.Lstat(fpath)
	if err != nil {
		return "", nil, err
	}

	if stat.IsDir() {
		if !params.Recursive {
			return "", nil, fmt.Errorf("%s is a directory, but Recursive option is not set", fpath)
		}
	}

	sf, err := files.NewSerialFile(fpath, params.Hidden, stat)
	return path.Base(fpath), sf, err
}

// Add imports files to the cluster from the given paths. A path can
// either be a local filesystem location or an web url (http:// or https://).
// In the latter case, the destination will be downloaded with a GET request.
// The AddParams allow to control different options, like enabling the
// sharding the resulting DAG across the IPFS daemons of multiple cluster
// peers. The output channel will receive regular updates as the adding
// process progresses.
func (c *defaultClient) Add(
	ctx context.Context,
	paths []string,
	params api.AddParams,
	out chan<- api.AddedOutput,
) error {
	ctx, span := trace.StartSpan(ctx, "client/Add")
	defer span.End()

	addFiles := make([]files.DirEntry, len(paths))
	for i, p := range paths {
		u, err := url.Parse(p)
		if err != nil {
			close(out)
			return fmt.Errorf("error parsing path: %s", err)
		}
		var name string
		var addFile files.Node
		if strings.HasPrefix(u.Scheme, "http") {
			addFile = files.NewWebFile(u)
			name = path.Base(u.Path)
		} else {
			if params.NoCopy {
				close(out)
				return fmt.Errorf("nocopy option is only valid for URLs")
			}
			name, addFile, err = makeSerialFile(p, params)
			if err != nil {
				close(out)
				return err
			}
		}
		addFiles[i] = files.FileEntry(name, addFile)
	}

	sliceFile := files.NewSliceDirectory(addFiles)
	// If `form` is set to true, the multipart data will have
	// a Content-Type of 'multipart/form-data', if `form` is false,
	// the Content-Type will be 'multipart/mixed'.
	return c.AddMultiFile(ctx, files.NewMultiFileReader(sliceFile, true), params, out)
}

// AddMultiFile imports new files from a MultiFileReader. See Add().
func (c *defaultClient) AddMultiFile(
	ctx context.Context,
	multiFileR *files.MultiFileReader,
	params api.AddParams,
	out chan<- api.AddedOutput,
) error {
	ctx, span := trace.StartSpan(ctx, "client/AddMultiFile")
	defer span.End()

	defer close(out)

	headers := make(map[string]string)
	headers["Content-Type"] = "multipart/form-data; boundary=" + multiFileR.Boundary()

	// This method must run with StreamChannels set.
	params.StreamChannels = true
	queryStr, err := params.ToQueryString()
	if err != nil {
		return err
	}

	// our handler decodes an AddedOutput and puts it
	// in the out channel.
	handler := func(dec *json.Decoder) error {
		if out == nil {
			return nil
		}
		var obj api.AddedOutput
		err := dec.Decode(&obj)
		if err != nil {
			return err
		}
		out <- obj
		return nil
	}

	err = c.doStream(ctx,
		"POST",
		"/add?"+queryStr,
		headers,
		multiFileR,
		handler,
	)
	return err
}
