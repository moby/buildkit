package ipfs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/ipfs-cluster/ipfs-cluster/api"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	cluster "github.com/ipfs-cluster/ipfs-cluster/api/rest/client"
	shell "github.com/ipfs/go-ipfs-api"

	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
)

const (
	attrClusterApi = "cluster_api"
	attrDaemonApi  = "daemon_api"
	attrPinName    = "pin_name"
	attrIPFSMfsDir = "mfs_dir"
)

type Config struct {
	ClusterApi string // Cluster API IP address and port to interact with the cluster (e.g. 127.0.0.1:9094)
	DaemonApi  string // IPFS daemon API address and port where Kubo is running (e.g. 127.0.0.1:5001)
	PinName    string // Name of the pin to identify the exported cache in the IPFS cluster (e.g. buildkit-cache)
	MFSDir     string // Mutable File System (MFS) directory path to store the cache (e.g. /buildkit-cache)
}

// ResolveCacheExporterFunc for "ipfs" cache exporter.
func ResolveCacheExporterFunc() remotecache.ResolveCacheExporterFunc {
	return func(ctx context.Context, g session.Group, attrs map[string]string) (remotecache.Exporter, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to create IPFS config")
		}

		clusterClient, err := newClusterClient(config)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to initialize IPFS cluster client")
		}

		cc := v1.NewCacheChains()
		return &exporter{
			CacheExporterTarget: cc,
			chains:              cc,
			config:              config,
			clusterClient:       &clusterClient,
		}, nil
	}
}

type exporter struct {
	solver.CacheExporterTarget
	chains        *v1.CacheChains
	config        *Config
	clusterClient *cluster.Client
}

func (ce *exporter) Name() string {
	return "exporting cache to IPFS"
}

func (ce *exporter) Finalize(ctx context.Context) (map[string]string, error) {
	clusterClient, err := newClusterClient(ce.config)
	if err != nil {
		return nil, err
	}

	prevPinCID, err := pinExists(ctx, ce.config.PinName, clusterClient)
	if err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp("", "ipfs-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	logrus.Debugf("created temp dir at %s to store manifest and blob(s)", tmpDir)
	if err := os.MkdirAll(filepath.Join(tmpDir, ce.config.MFSDir), os.ModePerm); err != nil {
		return nil, err
	}

	sh := shell.NewShell(ce.config.DaemonApi)
	exists, err := blobExists(ctx, sh, ce.config.MFSDir)
	if err != nil {
		logrus.Debugf("path %s does not exist, exiting because of: %s", ce.config.MFSDir, err)
		return nil, err
	}

	if exists {
		logrus.Debugf("path %s exists, removing it...", ce.config.MFSDir)
		if err := sh.FilesRm(ctx, ce.config.MFSDir, true); err != nil {
			return nil, err
		}
	}

	logrus.Debugf("creating path %s", ce.config.MFSDir)
	if err := sh.FilesMkdir(ctx, ce.config.MFSDir); err != nil {
		return nil, err
	}

	config, descs, err := ce.chains.Marshal(ctx)
	if err != nil {
		return nil, err
	}

	for i, l := range config.Layers {
		dgstPair, ok := descs[l.Blob]
		if !ok {
			return nil, errors.Errorf("missing blob %s", l.Blob)
		}
		if dgstPair.Descriptor.Annotations == nil {
			return nil, errors.Errorf("invalid descriptor without annotations")
		}
		var diffID digest.Digest
		v, ok := dgstPair.Descriptor.Annotations["containerd.io/uncompressed"]
		if !ok {
			return nil, errors.Errorf("invalid descriptor without uncompressed annotation")
		}
		dgst, err := digest.Parse(v)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse uncompressed annotation")
		}
		diffID = dgst

		path := blobKey(ce.config, dgstPair.Descriptor.Digest.String())
		exists, err := blobExists(ctx, sh, path)
		if err != nil {
			return nil, err
		}

		logrus.Debugf("layers %s exists = %t", path, exists)

		if !exists {
			layerDone := progress.OneOff(ctx, fmt.Sprintf("writing layer %s", l.Blob))
			dt, err := content.ReadBlob(ctx, dgstPair.Provider, dgstPair.Descriptor)
			if err != nil {
				return nil, layerDone(err)
			}

			if err := writeToTmp(filepath.Join(tmpDir, path), dt); err != nil {
				return nil, layerDone(err)
			}

			_ = layerDone(nil)
			logrus.Infof("wrote layer %s", l.Blob)
		}

		la := &v1.LayerAnnotations{
			DiffID:    diffID,
			Size:      dgstPair.Descriptor.Size,
			MediaType: dgstPair.Descriptor.MediaType,
		}
		if v, ok := dgstPair.Descriptor.Annotations["buildkit/createdat"]; ok {
			var t time.Time
			if err := (&t).UnmarshalText([]byte(v)); err != nil {
				return nil, err
			}
			la.CreatedAt = t.UTC()
		}
		config.Layers[i].Annotations = la
	}

	dt, err := json.Marshal(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal config")
	}

	manifestFilePath := manifestKey(ce.config, "manifest")
	if err := writeToTmp(filepath.Join(tmpDir, manifestFilePath), dt); err != nil {
		return nil, errors.Wrapf(err, "error writing manifest")
	}

	newCID, err := addDirToCluster(filepath.Join(tmpDir, ce.config.MFSDir), ce.config.PinName, clusterClient)
	if err != nil {
		return nil, err
	}

	if err := copyFilesToMFSDir(ctx, *newCID, ce.config.MFSDir, sh); err != nil {
		return nil, err
	}

	// If there's a stale cache pinned in the IPFS cluster, update the pin with the new CID and unpin the previous one
	if prevPinCID != nil && prevPinCID.Cid != newCID.Cid {
		if err := updatePin(ctx, ce.config.PinName, prevPinCID, newCID, clusterClient); err != nil {
			return nil, err
		}

		logrus.Debugf("unpinning previous pin: %s\n", prevPinCID.Cid)
		_, err = clusterClient.Unpin(ctx, *prevPinCID)
		if err != nil {
			return nil, err
		}
	}

	return nil, nil
}

// updatePin adds a new pin to the cluster taking all the options from an existing one.
func updatePin(ctx context.Context, pin string, oldCID *api.Cid, newCID *api.Cid, clusterCli cluster.Client) error {
	logrus.Debugf("Updating previous pin %s -> %s\n", *oldCID, *newCID)

	opts := api.PinOptions{
		ReplicationFactorMin: -1,
		ReplicationFactorMax: -1,
		Name:                 pin,
		PinUpdate:            *oldCID,
	}

	_, err := clusterCli.Pin(ctx, *newCID, opts)

	return err
}

// pinExists checks if the IPFS daemon has pinned the item.
// If it does, the function returns the CID, otherwise it returns nil.
func pinExists(ctx context.Context, pinName string, clusterCli cluster.Client) (*api.Cid, error) {
	logrus.Debugf("checking if IPFS daemon has pinned the item %s", pinName)

	out := make(chan api.GlobalPinInfo)
	done := make(chan bool)
	var cid *api.Cid
	go func() {
		for {
			j, more := <-out
			if more && j.Name == pinName {
				logrus.Debugf("pin status retrieved: %+v", j)
				cid = &j.Cid
			} else {
				logrus.Debug("check completed")
				done <- true
				return
			}
		}
	}()

	err := clusterCli.StatusAll(ctx, api.TrackerStatusPinned, true, out)
	if err != nil {
		return nil, err
	}

	<-done

	logrus.Debugf("pinned CID: %+v", cid)
	return cid, nil
}

// copyFilesToMFSDir adds references to the cache directory (identified by a CID) in MFS.
func copyFilesToMFSDir(ctx context.Context, newCID api.Cid, mfsDir string, sh *shell.Shell) error {
	logrus.Debugf("checking if MFS directory %s already exists", mfsDir)
	mfsDirExists := true
	if _, err := sh.FilesStat(ctx, mfsDir); err != nil {
		if strings.Contains(err.Error(), "file does not exist") {
			mfsDirExists = false
		} else {
			return err
		}
	}

	if mfsDirExists {
		logrus.Debugf("MFS directory %s already exists, removing it as it may contain stale files", mfsDir)
		if err := sh.FilesRm(ctx, mfsDir, true); err != nil {
			return err
		}
	}

	logrus.Debugf("copying cache directory with CID %s to MFS directory %s", newCID.Cid, mfsDir)
	return sh.FilesCp(ctx, "/ipfs/"+newCID.Cid.String(), mfsDir)
}

// addDirToCluster adds recursively the cache files present in a directory and pin them to the cluster.
func addDirToCluster(dir, pinName string, clusterCli cluster.Client) (*api.Cid, error) {
	logrus.Debugf("adding directory %s to IPFS and pinning it in the cluster under the pin %s...", dir, pinName)

	out := make(chan api.AddedOutput)
	done := make(chan bool)
	var cid api.Cid
	go func() {
		for {
			j, more := <-out
			if more {
				logrus.Debugf("added item: %+v", j)
				cid = j.Cid
			} else {
				logrus.Debugf("added all items")
				done <- true
				return
			}
		}
	}()

	err := clusterCli.Add(context.Background(), []string{dir}, api.AddParams{
		Recursive: true,
		PinOptions: api.PinOptions{
			ReplicationFactorMin: -1,
			ReplicationFactorMax: -1,
			Name:                 pinName,
		},
		IPFSAddParams: api.IPFSAddParams{
			CidVersion: 1,
			Progress:   false,
		},
	}, out)
	if err != nil {
		return nil, err
	}

	<-done

	logrus.Debugf("pinned CID: %+v", cid)
	return &cid, nil
}

func (ce *exporter) Config() remotecache.Config {
	return remotecache.Config{
		Compression: compression.New(compression.Default),
	}
}

func getConfig(attrs map[string]string) (*Config, error) {
	clusterApi, ok := attrs[attrClusterApi]
	if !ok {
		clusterApi, ok = os.LookupEnv("BUILDKIT_IPFS_CLUSTER_API")
		if !ok {
			return &Config{}, errors.New("either ${BUILDKIT_IPFS_CLUSTER_API} or cluster_api attribute is required for IPFS cache")
		}
	}

	daemonApi, ok := attrs[attrDaemonApi]
	if !ok {
		daemonApi, ok = os.LookupEnv("BUILDKIT_IPFS_DAEMON_API")
		if !ok {
			return &Config{}, errors.New("either ${BUILDKIT_IPFS_DAEMON_API} or daemon_api attribute is required for IPFS cache")
		}
	}

	pinName, ok := attrs[attrPinName]
	if !ok {
		pinName = "buildkit-cache"
	}

	mfsDir, ok := attrs[attrIPFSMfsDir]
	if !ok {
		mfsDir = "/buildkit-cache"
	}

	config := Config{
		ClusterApi: clusterApi,
		DaemonApi:  daemonApi,
		MFSDir:     mfsDir,
		PinName:    pinName,
	}

	return &config, nil
}

func writeToTmp(path string, dt []byte) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		logrus.Debugf("path %s does not exist, creating it...", dir)
		_ = os.MkdirAll(dir, 0666)
	}

	return os.WriteFile(path, dt, 0666)
}

type importer struct {
	clusterClient *cluster.Client
	ipfsClient    *shell.Shell
	config        *Config
}

// ResolveCacheImporterFunc for "ipfs" cache importer.
func ResolveCacheImporterFunc() remotecache.ResolveCacheImporterFunc {
	return func(ctx context.Context, g session.Group, attrs map[string]string) (remotecache.Importer, ocispecs.Descriptor, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, ocispecs.Descriptor{}, errors.WithMessage(err, "failed to create IPFS config")
		}

		clusterClient, err := newClusterClient(config)
		if err != nil {
			return nil, ocispecs.Descriptor{}, errors.WithMessage(err, "failed to initialize IPFS cluster config")
		}

		importer := &importer{
			clusterClient: &clusterClient,
			ipfsClient:    shell.NewShell(config.DaemonApi),
			config:        config,
		}

		return importer, ocispecs.Descriptor{}, nil
	}
}

func (i *importer) Resolve(ctx context.Context, _ ocispecs.Descriptor, id string, w worker.Worker) (solver.CacheManager, error) {
	cc, err := i.load(ctx)
	if err != nil {
		return nil, err
	}

	keysStorage, resultStorage, err := v1.NewCacheKeyStorage(cc, w)
	if err != nil {
		return nil, err
	}

	return solver.NewCacheManager(ctx, id, keysStorage, resultStorage), nil
}

func (i *importer) load(ctx context.Context) (*v1.CacheChains, error) {
	logrus.Debug("loading manifest")

	pinnedCID, err := pinExists(ctx, i.config.PinName, *i.clusterClient)
	if err != nil {
		return nil, err
	}

	mfsDir, err := i.ipfsClient.FilesStat(ctx, i.config.MFSDir)
	if err != nil && !strings.Contains(err.Error(), "file does not exist") {
		return nil, err
	}

	if mfsDir != nil {
		logrus.Debugf("comparing MFS dir CID (%s) against pinned CID (%s)", mfsDir.Hash, pinnedCID.String())
		if mfsDir.Hash != pinnedCID.String() {
			logrus.Debugf("cache is not up-to-date, removing the old MFS...")
			if err := i.ipfsClient.FilesRm(ctx, i.config.MFSDir, true); err != nil {
				return nil, err
			}
		}
	}

	if err := copyFilesToMFSDir(ctx, *pinnedCID, i.config.MFSDir, i.ipfsClient); err != nil {
		return nil, err
	}

	name := "manifest"
	path := manifestKey(i.config, name)
	exists, err := blobExists(ctx, i.ipfsClient, path)
	if err != nil {
		return nil, err
	}

	logrus.Debugf("name %s cache with path %s exists = %v", name, path, exists)

	if !exists {
		return v1.NewCacheChains(), nil
	}

	res, err := i.ipfsClient.FilesRead(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "error reading file from IPFS")
	}

	bytes, err := io.ReadAll(res)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	logrus.Debugf("imported config: %s", string(bytes))

	var config v1.CacheConfig
	if err := json.Unmarshal(bytes, &config); err != nil {
		return nil, errors.WithStack(err)
	}

	allLayers := v1.DescriptorProvider{}

	for _, l := range config.Layers {
		dpp, err := i.makeDescriptorProviderPair(l)
		if err != nil {
			return nil, err
		}
		allLayers[l.Blob] = *dpp
	}

	progress.OneOff(ctx, fmt.Sprintf("found %d layers in cache", len(allLayers)))(nil)

	cc := v1.NewCacheChains()
	if err := v1.ParseConfig(config, allLayers, cc); err != nil {
		return nil, err
	}
	return cc, nil
}

func (i *importer) makeDescriptorProviderPair(l v1.CacheLayer) (*v1.DescriptorProviderPair, error) {
	if l.Annotations == nil {
		return nil, errors.Errorf("cache layer with missing annotations")
	}
	if l.Annotations.DiffID == "" {
		return nil, errors.Errorf("cache layer with missing diffid")
	}
	annotations := map[string]string{}
	annotations["containerd.io/uncompressed"] = l.Annotations.DiffID.String()
	if !l.Annotations.CreatedAt.IsZero() {
		txt, err := l.Annotations.CreatedAt.MarshalText()
		if err != nil {
			return nil, err
		}
		annotations["buildkit/createdat"] = string(txt)
	}

	desc := ocispecs.Descriptor{
		MediaType:   l.Annotations.MediaType,
		Digest:      l.Blob,
		Size:        l.Annotations.Size,
		Annotations: annotations,
	}
	return &v1.DescriptorProviderPair{
		Provider: &ciProvider{
			desc:       desc,
			ipfsClient: i.ipfsClient,
			Provider:   contentutil.FromFetcher(&fetcher{ipfsClient: i.ipfsClient, config: i.config}),
			config:     i.config,
		},
		Descriptor: desc,
	}, nil
}

type ciProvider struct {
	content.Provider
	desc       ocispecs.Descriptor
	ipfsClient *shell.Shell
	config     *Config
	checkMutex sync.Mutex
	checked    bool
}

type fetcher struct {
	ipfsClient *shell.Shell
	config     *Config
}

func (f *fetcher) Fetch(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	path := blobKey(f.config, desc.Digest.String())
	exists, err := blobExists(ctx, f.ipfsClient, path)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, errors.Errorf("blob %s not found", desc.Digest)
	}

	logrus.Debugf("reading layer from cache: %s", path)

	res, err := f.ipfsClient.FilesRead(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "error reading file from IPFS")
	}

	return res, nil
}

// newClusterClient creates a new cluster client to interact with the IPFS cluster.
func newClusterClient(config *Config) (cluster.Client, error) {
	logrus.Debugf("initializing cluster client with endpoint %s", config.ClusterApi)

	s := strings.Split(config.ClusterApi, ":")
	if len(s) != 2 {
		return nil, fmt.Errorf("failed to split %s by colon", config.ClusterApi)
	}

	cfg := &cluster.Config{
		Host: s[0],
		Port: s[1],
	}
	return cluster.NewDefaultClient(cfg)
}

func manifestKey(config *Config, name string) string {
	return filepath.Join(config.MFSDir, "manifests", name)
}

func blobKey(config *Config, digest string) string {
	return filepath.Join(config.MFSDir, "blobs", digest)
}

func blobExists(ctx context.Context, ipfsClient *shell.Shell, path string) (bool, error) {
	if _, err := ipfsClient.FilesStat(ctx, path); err != nil {
		if strings.Contains(err.Error(), "file does not exist") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
