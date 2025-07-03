package main

import (
	"context"
	"encoding/json"
	"expvar"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/cachedigest"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/trace"
)

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
			w.Write([]byte(rec.Digest.String() + " (" + rec.Type.String() + "):\n"))
			for _, subRec := range rec.SubRecords {
				w.Write([]byte("  " + subRec.Digest.String() + " (" + subRec.Type.String() + "):\n"))
			}
			for _, frame := range rec.Data {
				switch frame.ID {
				case cachedigest.FrameIDData:
					w.Write([]byte("  " + frame.ID.String() + ": " + string(frame.Data) + "\n"))
				case cachedigest.FrameIDSkip:
					w.Write([]byte("  skipping " + string(frame.Data) + " bytes\n"))
				}
			}
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
		w.Write([]byte(record.Digest.String() + " (" + record.Type.String() + "):\n"))
		for _, subRec := range record.SubRecords {
			w.Write([]byte("  " + subRec.Digest.String() + " (" + subRec.Type.String() + "):\n"))
		}
		for _, frame := range record.Data {
			switch frame.ID {
			case cachedigest.FrameIDData:
				w.Write([]byte("  " + frame.ID.String() + ": " + string(frame.Data) + "\n"))
			case cachedigest.FrameIDSkip:
				w.Write([]byte("  skipping " + string(frame.Data) + " bytes\n"))
			}
		}
	}
}

func cacheRecordLookup(ctx context.Context, dgst digest.Digest) (*cachedigest.Record, error) {
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
