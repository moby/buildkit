package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/moby/buildkit/util/db/compaction"
	"github.com/moby/buildkit/util/disk"
	"github.com/pkg/errors"
)

func handleCompactionStatus(w http.ResponseWriter, r *http.Request) {
	type status struct {
		compaction.Status
		Available int64
		Required  int64
		Reason    string
	}
	result := map[string]status{}
	for path, scheduler := range compaction.Files() {
		s, err := scheduler.Inspect()
		entry := status{Status: s}
		if err != nil {
			entry.Reason = err.Error()
		} else {
			entry.Required = s.Stats.RequiredSpace()
			space, err := disk.GetDiskStat(filepath.Dir(path))
			if err != nil {
				entry.Reason = err.Error()
			} else {
				entry.Available = space.Available
				p := s.Config.MinReclaimPercent
				switch {
				case s.Stats.Size <= 0 || s.Stats.Reclaimable < s.Config.MinReclaimBytes || s.Stats.Reclaimable < s.Stats.Size/100*p+(s.Stats.Size%100*p+99)/100:
					entry.Reason = "reclaimable space below threshold"
				case entry.Available < entry.Required:
					entry.Reason = "insufficient free disk space"
				default:
					entry.Reason = "eligible for a manual attempt after idle"
				}
			}
		}
		result[path] = entry
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func handleCompactionRequest(w http.ResponseWriter, r *http.Request) {
	var origin http.CrossOriginProtection
	if err := origin.Check(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	scheduler, ok := compaction.Files()[r.URL.Query().Get("database")]
	if !ok {
		http.Error(w, "unknown database; GET /debug/compaction lists available databases", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(context.Canceled)
	req, err := scheduler.Request(ctx)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, compaction.ErrBusy) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	write := func(message string) error {
		if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, message); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err := write("manual attempt requested; this is not a dry run"); err != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-req.Events():
			if err := write(event); err != nil {
				return
			}
		case outcome := <-req.Done():
			for {
				select {
				case event := <-req.Events():
					if err := write(event); err != nil {
						return
					}
				default:
					result := outcome.Result
					_ = write(fmt.Sprintf("completed: compacted=%t before=%d after=%d reclaimed=%d copy_duration=%s reason=%q error=%q", result.Compacted, result.SizeBefore, result.SizeAfter, result.SizeBefore-result.SizeAfter, result.Duration, result.Reason, outcome.Error))
					return
				}
			}
		}
	}
}
