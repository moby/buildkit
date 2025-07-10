package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"expvar"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	cacheimport "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/cachedigest"
	"github.com/moby/buildkit/util/cachestore"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/trace"
)

var cacheStoreForDebug solver.CacheKeyStorage

func setupDebugHandlers(addr string) error {
	m := http.NewServeMux()
	m.Handle("/debug/vars", expvar.Handler())
	m.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	m.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	m.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	m.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	m.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
	m.Handle("/debug/requests", http.HandlerFunc(trace.Traces))
	m.Handle("/debug/events", http.HandlerFunc(trace.Events))
	m.Handle("/debug/cache/all", http.HandlerFunc(handleCacheAll))
	m.Handle("/debug/cache/lookup", http.HandlerFunc(handleCacheLookup))
	m.Handle("/debug/cache/store", http.HandlerFunc(handleDebugCacheStore))
	m.Handle("POST /debug/cache/load", http.HandlerFunc(handleCacheLoad))

	m.Handle("/debug/gc", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		runtime.GC()
		bklog.G(req.Context()).Debugf("triggered GC from debug endpoint")
	}))

	m.Handle("/metrics", promhttp.Handler())

	setupDebugFlight(m)

	// setting debugaddr is opt-in. permission is defined by listener address
	trace.AuthRequest = func(_ *http.Request) (bool, bool) {
		return true, true
	}

	if !strings.Contains(addr, "://") {
		addr = "tcp://" + addr
	}
	l, err := getListener(addr, os.Getuid(), os.Getgid(), "", nil, false)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              l.Addr().String(),
		Handler:           m,
		ReadHeaderTimeout: time.Minute,
	}
	bklog.L.Debugf("debug handlers listening at %s", addr)
	go func() {
		if err := server.Serve(l); err != nil {
			bklog.L.Errorf("failed to serve debug handlers: %v", err)
		}
	}()
	return nil
}

func handleCacheAll(w http.ResponseWriter, r *http.Request) {
	records, err := loadCacheAll(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch r.Header.Get("Accept") {
	case "application/json":
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(records)
	default:
		w.Header().Set("Content-Type", "text/plain")
		for _, rec := range records {
			printCacheRecord(rec, w)
			w.Write([]byte("\n"))
		}
	}
}

func handleCacheLookup(w http.ResponseWriter, r *http.Request) {
	dgstStr := r.URL.Query().Get("digest")
	if dgstStr == "" {
		http.Error(w, "digest query parameter is required", http.StatusBadRequest)
		return
	}

	dgst, err := digest.Parse(dgstStr)
	if err != nil {
		http.Error(w, "invalid digest: "+err.Error(), http.StatusBadRequest)
		return
	}

	record, err := cacheRecordLookup(r.Context(), dgst)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch r.Header.Get("Accept") {
	case "application/json":
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(record); err != nil {
			http.Error(w, "failed to encode record: "+err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		w.Header().Set("Content-Type", "text/plain")
		printCacheRecord(record, w)
	}
}

func printCacheRecord(record *cachedigest.Record, w io.Writer) {
	w.Write([]byte(record.Digest.String() + " (" + record.Type.String() + "):\n"))
	for _, subRec := range record.SubRecords {
		w.Write([]byte("  " + subRec.Digest.String() + " (" + subRec.Type.String() + "):\n"))
	}
	for _, frame := range record.Data {
		switch frame.ID {
		case cachedigest.FrameIDData:
			w.Write([]byte("  " + frame.ID.String() + ": " + string(frame.Data) + "\n"))
		case cachedigest.FrameIDSkip:
			fmt.Fprintf(w, "  skipping %d bytes\n", binary.LittleEndian.Uint32(frame.Data))
		}
	}
	for _, subRec := range record.SubRecords {
		w.Write([]byte("\n"))
		printCacheRecord(subRec, w)
	}
}

func cacheRecordLookup(ctx context.Context, dgst digest.Digest) (*cachedigest.Record, error) {
	if dgst == "sha256:8a5edab282632443219e051e4ade2d1d5bbc671c781051bf1437897cbdfea0f1" {
		return &cachedigest.Record{
			Digest: dgst,
			Type:   cachedigest.TypeString,
			Data: []cachedigest.Frame{
				{
					ID:   cachedigest.FrameIDData,
					Data: []byte("/"),
				},
			},
		}, nil
	}

	db := cachedigest.GetDefaultDB()
	typ, frames, err := db.Get(ctx, dgst.String())
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get digest %s from cache", dgst.String())
	}
	record := &cachedigest.Record{
		Digest: dgst,
		Type:   typ,
		Data:   frames,
	}
	if err := record.LoadSubRecords(func(d digest.Digest) (cachedigest.Type, []cachedigest.Frame, error) {
		typ, frames, err := db.Get(ctx, d.String())
		if err != nil {
			return "", nil, errors.Wrapf(err, "failed to load sub-record for %s", d.String())
		}
		return typ, frames, nil
	}); err != nil {
		return nil, errors.Wrapf(err, "failed to load sub-records for %s", dgst.String())
	}
	return record, nil
}

func loadCacheAll(ctx context.Context) ([]*cachedigest.Record, error) {
	var records []*cachedigest.Record
	m := map[digest.Digest]*cachedigest.Record{}
	db := cachedigest.GetDefaultDB()
	err := db.All(ctx, func(key string, typ cachedigest.Type, frames []cachedigest.Frame) error {
		dgst, err := digest.Parse(key)
		if err != nil {
			return errors.Wrapf(err, "failed to parse digest %q", key)
		}
		r := &cachedigest.Record{
			Digest: dgst,
			Type:   typ,
			Data:   frames,
		}
		records = append(records, r)
		m[dgst] = r
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, rec := range records {
		if err := rec.LoadSubRecords(func(d digest.Digest) (cachedigest.Type, []cachedigest.Frame, error) {
			rec, ok := m[d]
			if !ok {
				return "", nil, errors.Errorf("digest %s not found in cache", d)
			}
			return rec.Type, rec.Data, nil
		}); err != nil {
			return nil, errors.Wrapf(err, "failed to load sub-records for %s", rec.Digest.String())
		}
	}
	return records, nil
}

func handleCacheLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Body == nil {
		http.Error(w, "body is required", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	recs, err := loadCacheFromReader(r.Context(), r.Body)
	if err != nil {
		http.Error(w, "failed to load cache: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeCacheRecordsResponse(w, r, recs)
}

func loadCacheFromReader(ctx context.Context, rdr io.Reader) ([]*recordWithDebug, error) {
	dt, err := io.ReadAll(rdr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read body")
	}

	allLayers := cacheimport.DescriptorProvider{}
	cc := cacheimport.NewCacheChains()
	if err := cacheimport.Parse(dt, allLayers, cc); err != nil {
		return nil, err
	}

	keyStorage, _, err := cacheimport.NewCacheKeyStorage(cc, nil)
	if err != nil {
		return nil, err
	}

	recs, err := debugCacheStore(ctx, keyStorage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to debug cache store")
	}

	return recs, nil
}

func handleDebugCacheStore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	store := cacheStoreForDebug
	if store == nil {
		http.Error(w, "Cache store is not initialized for debug", http.StatusInternalServerError)
	}

	recs, err := debugCacheStore(r.Context(), store)
	if err != nil {
		http.Error(w, "Failed to debug cache store: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeCacheRecordsResponse(w, r, recs)
}

func writeCacheRecordsResponse(w http.ResponseWriter, r *http.Request, recs []*recordWithDebug) {
	w.WriteHeader(http.StatusOK)

	switch r.Header.Get("Accept") {
	case "application/json":
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(recs); err != nil {
			http.Error(w, "Failed to encode cache records: "+err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		w.Header().Set("Content-Type", "text/plain")
		for _, rec := range recs {
			randomSuffix := ""
			if rec.Random {
				randomSuffix = " (random)"
			}
			fmt.Fprintf(w, "ID: %d%s\n", rec.ID, randomSuffix)
			if rec.Digest != "" {
				fmt.Fprintf(w, "Digest: %s\n", rec.Digest)
			}
			if len(rec.Parents) > 0 {
				fmt.Fprintln(w, "Parents:")
				for input := range rec.Parents {
					ids := slices.Collect(maps.Keys(rec.ParentIDs[input]))
					s := make([]string, len(ids))
					for i, id := range ids {
						s[i] = fmt.Sprintf("%d", id)
					}
					fmt.Fprintf(w, "  Input %d:\t %s\n", input, strings.Join(s, ", "))
				}
			}
			if len(rec.Children) > 0 {
				fmt.Fprintln(w, "Children:")
				for _, child := range rec.Children {
					fmt.Fprintf(w, "  %d %s (input %d, output %d)\n", child.Record.ID, child.Digest, child.Input, child.Output)
					if child.Selector != "" {
						fmt.Fprintf(w, "    Selector: %s\n", child.Selector)
					}
				}
			}
			if len(rec.Debug) > 0 {
				fmt.Fprintln(w, "Plaintexts:")
				for _, debugRec := range rec.Debug {
					printCacheRecord(debugRec, w)
					w.Write([]byte("\n"))
				}
			}
			w.Write([]byte("\n"))
		}
	}
}

type recordWithDebug struct {
	*cachestore.Record
	Debug []*cachedigest.Record `json:"debug,omitempty"`
}

func debugCacheStore(ctx context.Context, store solver.CacheKeyStorage) ([]*recordWithDebug, error) {
	recs, err := cachestore.Records(ctx, store)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cache records")
	}

	recsWithDebug := make([]*recordWithDebug, len(recs))
	for i, rec := range recs {
		debugRec := &recordWithDebug{
			Record: rec,
		}
		m := map[digest.Digest]*cachedigest.Record{}
		if rec.Digest != "" {
			m[rec.Digest] = nil
		}
		for _, link := range rec.Children {
			m[link.Digest] = nil
			if link.Selector != "" {
				m[link.Selector] = nil
			}
		}
		for dgst := range m {
			cr, err := cacheRecordLookup(ctx, dgst)
			if err != nil {
				bklog.L.Errorf("failed to lookup cache record for %s: %v", dgst, err)
				continue
			}
			m[dgst] = cr
		}
		for _, cr := range m {
			if cr != nil {
				debugRec.Debug = append(debugRec.Debug, cr)
			}
		}
		recsWithDebug[i] = debugRec
	}

	return recsWithDebug, nil
}
