package progressui

import (
	"context"
	"fmt"
	"time"

	"github.com/containerd/console"
	"github.com/docker/go-units"
	"github.com/morikuni/aec"
	digest "github.com/opencontainers/go-digest"
	"github.com/tonistiigi/buildkit_poc/client"
)

func DisplaySolveStatus(ctx context.Context, ch chan *client.SolveStatus) error {
	disp := &display{c: console.Current()}

	t := newTrace()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var done bool

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case ss, ok := <-ch:
			if ok {
				t.update(ss) // rate limit
			} else {
				done = true
			}
		}
		disp.print(t.displayInfo(-1))
		if done {
			return nil
		}
	}
}

type displayInfo struct {
	startTime      time.Time
	jobs           []job
	countTotal     int
	countCompleted int
}

type job struct {
	startTime     *time.Time
	completedTime *time.Time
	name          string
}

type trace struct {
	localTimeDiff time.Duration
	vertexes      []*vertex
	byDigest      map[digest.Digest]*vertex
}

type vertex struct {
	*client.Vertex
	statuses []*status
	byID     map[string]*status
}

type status struct {
	*client.VertexStatus
}

func newTrace() *trace {
	return &trace{
		byDigest: make(map[digest.Digest]*vertex),
	}
}

func (t *trace) update(s *client.SolveStatus) {
	for _, v := range s.Vertexes {
		prev, ok := t.byDigest[v.Digest]
		if !ok {
			t.byDigest[v.Digest] = &vertex{
				byID: make(map[string]*status),
			}
		}
		if v.Started != nil && (prev == nil || prev.Started == nil) {
			if t.localTimeDiff == 0 {
				t.localTimeDiff = time.Since(*v.Started)
			}
			t.vertexes = append(t.vertexes, t.byDigest[v.Digest])
		}
		t.byDigest[v.Digest].Vertex = v
	}
	for _, s := range s.Statuses {
		v, ok := t.byDigest[s.Vertex]
		if !ok {
			continue // shouldn't happen
		}
		prev, ok := v.byID[s.ID]
		if !ok {
			v.byID[s.ID] = &status{VertexStatus: s}
		}
		if s.Started != nil && (prev == nil || prev.Started == nil) {
			v.statuses = append(v.statuses, v.byID[s.ID])
		}
		v.byID[s.ID].VertexStatus = s
	}
}

func (t *trace) displayInfo(maxRows int) (d displayInfo) {
	d.startTime = time.Now()
	if t.localTimeDiff != 0 {
		d.startTime = (*t.vertexes[0].Started).Add(t.localTimeDiff)
	}
	d.countTotal = len(t.byDigest)
	for _, v := range t.byDigest {
		if v.Completed != nil {
			d.countCompleted++
		}
	}

	for _, v := range t.vertexes {
		j := job{
			startTime:     addTime(v.Started, t.localTimeDiff),
			completedTime: addTime(v.Completed, t.localTimeDiff),
			name:          v.Name,
		}
		d.jobs = append(d.jobs, j)
		for _, s := range v.statuses {
			j := job{
				startTime:     addTime(s.Started, t.localTimeDiff),
				completedTime: addTime(s.Completed, t.localTimeDiff),
				name:          "=> " + s.ID,
			}
			if s.Total != 0 {
				j.name += " " + units.HumanSize(float64(s.Current)) + " / " + units.HumanSize(float64(s.Current))
			}
			d.jobs = append(d.jobs, j)
		}
	}

	return d
}

func addTime(tm *time.Time, d time.Duration) *time.Time {
	if tm == nil {
		return nil
	}
	t := (*tm).Add(d)
	return &t
}

type display struct {
	c         console.Console
	lineCount int
	repeated  bool
}

func (disp *display) print(d displayInfo) {
	width := 60
	size, err := disp.c.Size()
	if err == nil {
		width = int(size.Width)
	}
	_ = width

	b := aec.EmptyBuilder
	for i := 0; i <= disp.lineCount; i++ {
		b = b.EraseLine(aec.EraseModes.All).Up(1)
	}
	if !disp.repeated {
		b = b.Down(1)
	}
	b = b.EraseLine(aec.EraseModes.All)
	disp.repeated = true
	fmt.Print(b.Column(0).ANSI)

	statusStr := ""
	if d.countCompleted > 0 && d.countCompleted == d.countTotal {
		statusStr = "FINISHED"
	}

	fmt.Printf("[+] Building %.1fs (%d/%d) %s\n", time.Since(d.startTime).Seconds(), d.countCompleted, d.countTotal, statusStr)
	for _, j := range d.jobs {
		endTime := time.Now()
		if j.completedTime != nil {
			endTime = *j.completedTime
		}
		dt := endTime.Sub(*j.startTime).Seconds()
		if dt < 0.05 {
			dt = 0
		}
		out := fmt.Sprintf(" => %s %.1fs\n", j.name, dt)
		if j.completedTime != nil {
			out = aec.Apply(out, aec.BlueF)
		}
		fmt.Print(out)
	}
	disp.lineCount = len(d.jobs)
}
