package client

import (
	"context"
	"sync/atomic"

	shell "github.com/ipfs/go-ipfs-api"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs-cluster/ipfs-cluster/api"
	peer "github.com/libp2p/go-libp2p/core/peer"
)

// loadBalancingClient is a client to interact with IPFS Cluster APIs
// that balances the load by distributing requests among peers.
type loadBalancingClient struct {
	strategy LBStrategy
	retries  int
}

// LBStrategy is a strategy to load balance requests among clients.
type LBStrategy interface {
	Next(count int) Client
	SetClients(clients []Client)
}

// RoundRobin is a load balancing strategy that would use clients in a sequence
// for all methods, throughout the lifetime of the lb client.
type RoundRobin struct {
	clients []Client
	counter uint32
	length  uint32
}

// Next return the next client to be used.
func (r *RoundRobin) Next(count int) Client {
	i := atomic.AddUint32(&r.counter, 1) % r.length

	return r.clients[i]
}

// SetClients sets a list of clients for this strategy.
func (r *RoundRobin) SetClients(cl []Client) {
	r.clients = cl
	r.length = uint32(len(cl))
}

// Failover is a load balancing strategy that would try the first cluster peer
// first. If the first call fails it would try other clients for that call in a
// round robin fashion.
type Failover struct {
	clients []Client
}

// Next returns the next client to be used.
func (f *Failover) Next(count int) Client {
	return f.clients[count%len(f.clients)]
}

// SetClients sets a list of clients for this strategy.
func (f *Failover) SetClients(cl []Client) {
	f.clients = cl
}

// NewLBClient returns a new client that would load balance requests among
// clients.
func NewLBClient(strategy LBStrategy, cfgs []*Config, retries int) (Client, error) {
	var clients []Client
	for _, cfg := range cfgs {
		defaultClient, err := NewDefaultClient(cfg)
		if err != nil {
			return nil, err
		}
		clients = append(clients, defaultClient)
	}
	strategy.SetClients(clients)
	return &loadBalancingClient{strategy: strategy, retries: retries}, nil
}

// retry tries the request until it is successful or tries `lc.retries` times.
func (lc *loadBalancingClient) retry(count int, call func(Client) error) error {
	logger.Debugf("retrying %d times", count+1)

	err := call(lc.strategy.Next(count))
	count++

	// successful request
	if err == nil {
		return nil
	}

	// It is a safety check. This error should never occur.
	// All errors returned by client methods are of type `api.Error`.
	apiErr, ok := err.(api.Error)
	if !ok {
		logger.Error("could not cast error into api.Error")
		return err
	}

	if apiErr.Code != 0 {
		return err
	}

	if count == lc.retries {
		logger.Errorf("reached maximum number of retries without success, retries: %d", lc.retries)
		return err
	}

	return lc.retry(count, call)
}

// ID returns information about the cluster Peer.
func (lc *loadBalancingClient) ID(ctx context.Context) (api.ID, error) {
	var id api.ID
	call := func(c Client) error {
		var err error
		id, err = c.ID(ctx)
		return err
	}

	err := lc.retry(0, call)
	return id, err
}

// Peers requests ID information for all cluster peers.
func (lc *loadBalancingClient) Peers(ctx context.Context, out chan<- api.ID) error {
	call := func(c Client) error {
		done := make(chan struct{})
		cout := make(chan api.ID, cap(out))
		go func() {
			for o := range cout {
				out <- o
			}
			done <- struct{}{}
		}()

		// this blocks until done
		err := c.Peers(ctx, cout)
		// wait for cout to be closed
		select {
		case <-ctx.Done():
		case <-done:
		}
		return err
	}

	// retries call as needed.
	err := lc.retry(0, call)
	close(out)
	return err
}

// PeerAdd adds a new peer to the cluster.
func (lc *loadBalancingClient) PeerAdd(ctx context.Context, pid peer.ID) (api.ID, error) {
	var id api.ID
	call := func(c Client) error {
		var err error
		id, err = c.PeerAdd(ctx, pid)
		return err
	}

	err := lc.retry(0, call)
	return id, err
}

// PeerRm removes a current peer from the cluster.
func (lc *loadBalancingClient) PeerRm(ctx context.Context, id peer.ID) error {
	call := func(c Client) error {
		return c.PeerRm(ctx, id)
	}

	return lc.retry(0, call)
}

// Pin tracks a Cid with the given replication factor and a name for
// human-friendliness.
func (lc *loadBalancingClient) Pin(ctx context.Context, ci api.Cid, opts api.PinOptions) (api.Pin, error) {
	var pin api.Pin
	call := func(c Client) error {
		var err error
		pin, err = c.Pin(ctx, ci, opts)
		return err
	}

	err := lc.retry(0, call)
	return pin, err
}

// Unpin untracks a Cid from cluster.
func (lc *loadBalancingClient) Unpin(ctx context.Context, ci api.Cid) (api.Pin, error) {
	var pin api.Pin
	call := func(c Client) error {
		var err error
		pin, err = c.Unpin(ctx, ci)
		return err
	}

	err := lc.retry(0, call)
	return pin, err
}

// PinPath allows to pin an element by the given IPFS path.
func (lc *loadBalancingClient) PinPath(ctx context.Context, path string, opts api.PinOptions) (api.Pin, error) {
	var pin api.Pin
	call := func(c Client) error {
		var err error
		pin, err = c.PinPath(ctx, path, opts)
		return err
	}

	err := lc.retry(0, call)
	return pin, err
}

// UnpinPath allows to unpin an item by providing its IPFS path.
// It returns the unpinned api.Pin information of the resolved Cid.
func (lc *loadBalancingClient) UnpinPath(ctx context.Context, p string) (api.Pin, error) {
	var pin api.Pin
	call := func(c Client) error {
		var err error
		pin, err = c.UnpinPath(ctx, p)
		return err
	}

	err := lc.retry(0, call)
	return pin, err
}

// Allocations returns the consensus state listing all tracked items and
// the peers that should be pinning them.
func (lc *loadBalancingClient) Allocations(ctx context.Context, filter api.PinType, out chan<- api.Pin) error {
	call := func(c Client) error {
		done := make(chan struct{})
		cout := make(chan api.Pin, cap(out))
		go func() {
			for o := range cout {
				out <- o
			}
			done <- struct{}{}
		}()

		// this blocks until done
		err := c.Allocations(ctx, filter, cout)
		// wait for cout to be closed
		select {
		case <-ctx.Done():
		case <-done:
		}
		return err
	}

	err := lc.retry(0, call)
	close(out)
	return err
}

// Allocation returns the current allocations for a given Cid.
func (lc *loadBalancingClient) Allocation(ctx context.Context, ci api.Cid) (api.Pin, error) {
	var pin api.Pin
	call := func(c Client) error {
		var err error
		pin, err = c.Allocation(ctx, ci)
		return err
	}

	err := lc.retry(0, call)
	return pin, err
}

// Status returns the current ipfs state for a given Cid. If local is true,
// the information affects only the current peer, otherwise the information
// is fetched from all cluster peers.
func (lc *loadBalancingClient) Status(ctx context.Context, ci api.Cid, local bool) (api.GlobalPinInfo, error) {
	var pinInfo api.GlobalPinInfo
	call := func(c Client) error {
		var err error
		pinInfo, err = c.Status(ctx, ci, local)
		return err
	}

	err := lc.retry(0, call)
	return pinInfo, err
}

// StatusCids returns Status() information for the given Cids. If local is
// true, the information affects only the current peer, otherwise the
// information is fetched from all cluster peers.
func (lc *loadBalancingClient) StatusCids(ctx context.Context, cids []api.Cid, local bool, out chan<- api.GlobalPinInfo) error {
	call := func(c Client) error {
		done := make(chan struct{})
		cout := make(chan api.GlobalPinInfo, cap(out))
		go func() {
			for o := range cout {
				out <- o
			}
			done <- struct{}{}
		}()

		// this blocks until done
		err := c.StatusCids(ctx, cids, local, cout)
		// wait for cout to be closed
		select {
		case <-ctx.Done():
		case <-done:
		}
		return err
	}

	err := lc.retry(0, call)
	close(out)
	return err
}

// StatusAll gathers Status() for all tracked items. If a filter is
// provided, only entries matching the given filter statuses
// will be returned. A filter can be built by merging TrackerStatuses with
// a bitwise OR operation (st1 | st2 | ...). A "0" filter value (or
// api.TrackerStatusUndefined), means all.
func (lc *loadBalancingClient) StatusAll(ctx context.Context, filter api.TrackerStatus, local bool, out chan<- api.GlobalPinInfo) error {
	call := func(c Client) error {
		done := make(chan struct{})
		cout := make(chan api.GlobalPinInfo, cap(out))
		go func() {
			for o := range cout {
				out <- o
			}
			done <- struct{}{}
		}()

		// this blocks until done
		err := c.StatusAll(ctx, filter, local, cout)
		// wait for cout to be closed
		select {
		case <-ctx.Done():
		case <-done:
		}
		return err
	}

	err := lc.retry(0, call)
	close(out)
	return err
}

// Recover retriggers pin or unpin ipfs operations for a Cid in error state.
// If local is true, the operation is limited to the current peer, otherwise
// it happens on every cluster peer.
func (lc *loadBalancingClient) Recover(ctx context.Context, ci api.Cid, local bool) (api.GlobalPinInfo, error) {
	var pinInfo api.GlobalPinInfo
	call := func(c Client) error {
		var err error
		pinInfo, err = c.Recover(ctx, ci, local)
		return err
	}

	err := lc.retry(0, call)
	return pinInfo, err
}

// RecoverAll triggers Recover() operations on all tracked items. If local is
// true, the operation is limited to the current peer. Otherwise, it happens
// everywhere.
func (lc *loadBalancingClient) RecoverAll(ctx context.Context, local bool, out chan<- api.GlobalPinInfo) error {
	call := func(c Client) error {
		done := make(chan struct{})
		cout := make(chan api.GlobalPinInfo, cap(out))
		go func() {
			for o := range cout {
				out <- o
			}
			done <- struct{}{}
		}()

		// this blocks until done
		err := c.RecoverAll(ctx, local, cout)
		// wait for cout to be closed
		select {
		case <-ctx.Done():
		case <-done:
		}
		return err
	}

	err := lc.retry(0, call)
	close(out)
	return err
}

// Alerts returns things that are wrong with cluster.
func (lc *loadBalancingClient) Alerts(ctx context.Context) ([]api.Alert, error) {
	var alerts []api.Alert
	call := func(c Client) error {
		var err error
		alerts, err = c.Alerts(ctx)
		return err
	}

	err := lc.retry(0, call)
	return alerts, err
}

// Version returns the ipfs-cluster peer's version.
func (lc *loadBalancingClient) Version(ctx context.Context) (api.Version, error) {
	var v api.Version
	call := func(c Client) error {
		var err error
		v, err = c.Version(ctx)
		return err
	}
	err := lc.retry(0, call)
	return v, err
}

// GetConnectGraph returns an ipfs-cluster connection graph.
// The serialized version, strings instead of pids, is returned.
func (lc *loadBalancingClient) GetConnectGraph(ctx context.Context) (api.ConnectGraph, error) {
	var graph api.ConnectGraph
	call := func(c Client) error {
		var err error
		graph, err = c.GetConnectGraph(ctx)
		return err
	}

	err := lc.retry(0, call)
	return graph, err
}

// Metrics returns a map with the latest valid metrics of the given name
// for the current cluster peers.
func (lc *loadBalancingClient) Metrics(ctx context.Context, name string) ([]api.Metric, error) {
	var metrics []api.Metric
	call := func(c Client) error {
		var err error
		metrics, err = c.Metrics(ctx, name)
		return err
	}

	err := lc.retry(0, call)
	return metrics, err
}

// MetricNames returns the list of metric types.
func (lc *loadBalancingClient) MetricNames(ctx context.Context) ([]string, error) {
	var metricNames []string
	call := func(c Client) error {
		var err error
		metricNames, err = c.MetricNames(ctx)
		return err
	}

	err := lc.retry(0, call)

	return metricNames, err
}

// RepoGC runs garbage collection on IPFS daemons of cluster peers and
// returns collected CIDs. If local is true, it would garbage collect
// only on contacted peer, otherwise on all peers' IPFS daemons.
func (lc *loadBalancingClient) RepoGC(ctx context.Context, local bool) (api.GlobalRepoGC, error) {
	var repoGC api.GlobalRepoGC

	call := func(c Client) error {
		var err error
		repoGC, err = c.RepoGC(ctx, local)
		return err
	}

	err := lc.retry(0, call)
	return repoGC, err
}

// Add imports files to the cluster from the given paths. A path can
// either be a local filesystem location or an web url (http:// or https://).
// In the latter case, the destination will be downloaded with a GET request.
// The AddParams allow to control different options, like enabling the
// sharding the resulting DAG across the IPFS daemons of multiple cluster
// peers. The output channel will receive regular updates as the adding
// process progresses.
func (lc *loadBalancingClient) Add(
	ctx context.Context,
	paths []string,
	params api.AddParams,
	out chan<- api.AddedOutput,
) error {
	call := func(c Client) error {
		done := make(chan struct{})
		cout := make(chan api.AddedOutput, cap(out))
		go func() {
			for o := range cout {
				out <- o
			}
			done <- struct{}{}
		}()

		// this blocks until done
		err := c.Add(ctx, paths, params, cout)
		// wait for cout to be closed
		select {
		case <-ctx.Done():
		case <-done:
		}
		return err
	}

	err := lc.retry(0, call)
	close(out)
	return err
}

// AddMultiFile imports new files from a MultiFileReader. See Add().
func (lc *loadBalancingClient) AddMultiFile(
	ctx context.Context,
	multiFileR *files.MultiFileReader,
	params api.AddParams,
	out chan<- api.AddedOutput,
) error {
	call := func(c Client) error {
		done := make(chan struct{})
		cout := make(chan api.AddedOutput, cap(out))
		go func() {
			for o := range cout {
				out <- o
			}
			done <- struct{}{}
		}()

		// this blocks until done
		err := c.AddMultiFile(ctx, multiFileR, params, cout)
		// wait for cout to be closed
		select {
		case <-ctx.Done():
		case <-done:
		}
		return err
	}

	err := lc.retry(0, call)
	close(out)
	return err
}

// IPFS returns an instance of go-ipfs-api's Shell, pointing to the
// configured ProxyAddr (or to the default Cluster's IPFS proxy port).
// It re-uses this Client's HTTP client, thus will be constrained by
// the same configurations affecting it (timeouts...).
func (lc *loadBalancingClient) IPFS(ctx context.Context) *shell.Shell {
	var s *shell.Shell
	call := func(c Client) error {
		s = c.IPFS(ctx)
		return nil
	}

	lc.retry(0, call)

	return s
}
