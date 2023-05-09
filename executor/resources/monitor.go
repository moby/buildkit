package resources

import (
	"bufio"
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/buildkit/executor/resources/types"
	"github.com/moby/buildkit/util/network"
	"golang.org/x/sys/unix"
)

const (
	cgroupProcsFile       = "cgroup.procs"
	cgroupControllersFile = "cgroup.controllers"
	cgroupSubtreeFile     = "cgroup.subtree_control"
	defaultMountpoint     = "/sys/fs/cgroup"
	initGroup             = "init"
)

var initOnce sync.Once
var isCgroupV2 bool

type cgroupRecord struct {
	once       sync.Once
	ns         string
	release    func(context.Context) error
	lastSample *types.Sample
	err        error
	done       chan struct{}
	monitor    *Monitor
	netSampler NetworkSampler
}

func (r *cgroupRecord) Wait() error {
	go r.close()
	<-r.done
	return r.err
}

func (r *cgroupRecord) close() {
	r.once.Do(func() {
		s, err := r.sample()
		if err != nil {
			r.err = err
		} else {
			r.lastSample = s
		}
		if err := r.release(context.Background()); err != nil {
			if r.err == nil {
				r.err = err
			}
		}
		close(r.done)
		go func() {
			r.monitor.mu.Lock()
			delete(r.monitor.records, r.ns)
			r.monitor.mu.Unlock()
		}()
	})
}

func (r *cgroupRecord) sample() (*types.Sample, error) {
	now := time.Now()
	cpu, err := getCgroupCPUStat(filepath.Join(defaultMountpoint, r.ns))
	if err != nil {
		return nil, err
	}
	memory, err := getCgroupMemoryStat(filepath.Join(defaultMountpoint, r.ns))
	if err != nil {
		return nil, err
	}
	io, err := getCgroupIOStat(filepath.Join(defaultMountpoint, r.ns))
	if err != nil {
		return nil, err
	}
	pids, err := getCgroupPIDsStat(filepath.Join(defaultMountpoint, r.ns))
	if err != nil {
		return nil, err
	}
	sample := &types.Sample{
		Timestamp:  now,
		CPUStat:    cpu,
		MemoryStat: memory,
		IOStat:     io,
		PIDsStat:   pids,
	}
	if r.netSampler != nil {
		net, err := r.netSampler.Sample()
		if err != nil {
			return nil, err
		}
		sample.NetStat = net
	}
	return sample, nil
}

func (r *cgroupRecord) Samples() ([]*types.Sample, error) {
	<-r.done
	if r.err != nil {
		return nil, r.err
	}
	return []*types.Sample{r.lastSample}, nil
}

type nopRecord struct {
}

func (r *nopRecord) Wait() error {
	return nil
}

func (r *nopRecord) Samples() ([]*types.Sample, error) {
	return nil, nil
}

type Monitor struct {
	mu      sync.Mutex
	closed  chan struct{}
	records map[string]*cgroupRecord
}

type NetworkSampler interface {
	Sample() (*network.Sample, error)
}

type RecordOpt struct {
	Release        func(context.Context) error
	NetworkSampler NetworkSampler
}

func (m *Monitor) RecordNamespace(ns string, opt RecordOpt) (types.Recorder, error) {
	isClosed := false
	select {
	case <-m.closed:
		isClosed = true
	default:
	}
	if !isCgroupV2 || isClosed {
		if err := opt.Release(context.TODO()); err != nil {
			return nil, err
		}
		return &nopRecord{}, nil
	}
	r := &cgroupRecord{
		ns:         ns,
		release:    opt.Release,
		done:       make(chan struct{}),
		monitor:    m,
		netSampler: opt.NetworkSampler,
	}
	m.mu.Lock()
	m.records[ns] = r
	m.mu.Unlock()
	go r.close()
	return r, nil
}

func (m *Monitor) Close() error {
	close(m.closed)
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.records {
		r.close()
	}
	return nil
}

func NewMonitor() (*Monitor, error) {
	initOnce.Do(func() {
		isCgroupV2 = isCgroup2()
		if !isCgroupV2 {
			return
		}
		if err := prepareCgroupControllers(); err != nil {
			log.Printf("failed to prepare cgroup controllers: %+v", err)
		}
	})

	return &Monitor{
		closed:  make(chan struct{}),
		records: make(map[string]*cgroupRecord),
	}, nil
}

func prepareCgroupControllers() error {
	v, ok := os.LookupEnv("BUILDKIT_SETUP_CGROUPV2_ROOT")
	if !ok {
		return nil
	}
	if b, _ := strconv.ParseBool(v); !b {
		return nil
	}
	// move current process to init cgroup
	if err := os.MkdirAll(filepath.Join(defaultMountpoint, initGroup), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(defaultMountpoint, cgroupProcsFile), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	s := bufio.NewScanner(f)
	for s.Scan() {
		if err := os.WriteFile(filepath.Join(defaultMountpoint, initGroup, cgroupProcsFile), s.Bytes(), 0); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	dt, err := os.ReadFile(filepath.Join(defaultMountpoint, cgroupControllersFile))
	if err != nil {
		return err
	}
	for _, c := range strings.Split(string(dt), " ") {
		if c == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(defaultMountpoint, cgroupSubtreeFile), []byte("+"+c), 0); err != nil {
			// ignore error
			log.Printf("failed to enable cgroup controller %q: %+v", c, err)
		}
	}
	return nil
}

func isCgroup2() bool {
	var st unix.Statfs_t
	err := unix.Statfs(defaultMountpoint, &st)
	if err != nil {
		return false
	}
	return st.Type == unix.CGROUP2_SUPER_MAGIC
}
