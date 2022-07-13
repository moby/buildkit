package cache

import (
	"sync"
	"time"
)

type GCRunAnalytics struct {
	Start            *time.Time
	End              *time.Time
	Success          bool
	NumRecordsBefore int64
	ClearedRecords   int64
	SizeBefore       int64
	ClearedSize      int64
}

const maxPastGCAnalytics = 10

type GCAnalytics struct {
	mu                 sync.Mutex
	current            GCRunAnalytics
	past               []GCRunAnalytics
	allTimeRuns        int64
	allTimeMaxDuration time.Duration
	allTimeDuration    time.Duration
}

type GCSummary struct {
	NumRuns            int
	NumFailures        int
	AvgDuration        time.Duration
	AvgRecordsCleared  int64
	AvgSizeCleared     int64
	AvgRecordsBefore   int64
	AvgSizeBefore      int64
	AllTimeRuns        int64
	AllTimeMaxDuration time.Duration
	AllTimeDuration    time.Duration
}

func (g *GCAnalytics) Start(numRecordsBefore int64, sizeBefore int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	g.current.Start = &now
	g.current.NumRecordsBefore = numRecordsBefore
	g.current.SizeBefore = sizeBefore
}

func (g *GCAnalytics) End(success bool, clearedRecords int64, clearedSize int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	duration := now.Sub(*g.current.Start)
	g.current.End = &now
	g.current.Success = success
	g.current.ClearedRecords = clearedRecords
	g.current.ClearedSize = clearedSize
	g.past = append(g.past, g.current)
	if len(g.past) > maxPastGCAnalytics {
		prev := g.past
		g.past = make([]GCRunAnalytics, 0, maxPastGCAnalytics)
		for _, run := range prev[1:] {
			g.past = append(g.past, run)
		}
	}
	g.current = GCRunAnalytics{}
	g.allTimeRuns++
	g.allTimeDuration += duration
	if g.allTimeDuration > g.allTimeMaxDuration {
		g.allTimeMaxDuration = g.allTimeDuration
	}
}

func (g *GCAnalytics) GetStats() (GCSummary, *GCRunAnalytics, *GCRunAnalytics) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sum := GCSummary{
		NumRuns:            len(g.past),
		AllTimeRuns:        g.allTimeRuns,
		AllTimeMaxDuration: g.allTimeMaxDuration,
		AllTimeDuration:    g.allTimeDuration,
	}
	var curRet *GCRunAnalytics
	var lastRet *GCRunAnalytics
	if g.current.Start != nil {
		cur := g.current
		curRet = &cur
	}
	if len(g.past) > 0 {
		last := g.past[len(g.past)-1]
		lastRet = &last
	}
	if len(g.past) == 0 {
		return sum, curRet, lastRet
	}
	for _, run := range g.past {
		if !run.Success {
			sum.NumFailures++
		}
		if run.Start != nil && run.End != nil {
			sum.AvgDuration += run.End.Sub(*run.Start)
		}
		sum.AvgRecordsCleared += run.ClearedRecords
		sum.AvgSizeCleared += run.ClearedSize
		sum.AvgRecordsBefore += run.NumRecordsBefore
		sum.AvgSizeBefore += run.SizeBefore
	}
	sum.AvgDuration /= time.Duration(len(g.past))
	sum.AvgRecordsCleared /= int64(len(g.past))
	sum.AvgSizeCleared /= int64(len(g.past))
	sum.AvgRecordsBefore /= int64(len(g.past))
	sum.AvgSizeBefore /= int64(len(g.past))
	return sum, curRet, lastRet
}
