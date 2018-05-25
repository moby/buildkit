package progressui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/containerd/console"
	"github.com/moby/buildkit/client"
	"github.com/morikuni/aec"
	digest "github.com/opencontainers/go-digest"
	"github.com/tonistiigi/units"
	"golang.org/x/time/rate"
)

func DisplaySolveStatus(ctx context.Context, c console.Console, ch chan *client.SolveStatus) error {
	disp := &display{c: c}

	t := newTrace()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	displayLimiter := rate.NewLimiter(rate.Every(70*time.Millisecond), 1)

	var done bool

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case ss, ok := <-ch:
			if ok {
				t.update(ss)
			} else {
				done = true
			}
		}

		if done {
			disp.print(t.displayInfo(), true)
			t.printErrorLogs(c)
			return nil
		} else if displayLimiter.Allow() {
			disp.print(t.displayInfo(), false)
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
	status        string
	hasError      bool
	isCanceled    bool
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
	logs     []*client.VertexLog
	indent   string
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
	for _, l := range s.Logs {
		v, ok := t.byDigest[l.Vertex]
		if !ok {
			continue // shouldn't happen
		}
		v.logs = append(v.logs, l)
	}
}

func (t *trace) printErrorLogs(f io.Writer) {
	for _, v := range t.vertexes {
		if v.Error != "" && !strings.HasSuffix(v.Error, context.Canceled.Error()) {
			fmt.Fprintln(f, "------")
			fmt.Fprintf(f, " > %s:\n", v.Name)
			for _, l := range v.logs {
				switch l.Stream {
				case 1:
					f.Write(l.Data)
				case 2:
					f.Write(l.Data)
				}
			}
			fmt.Fprintln(f, "------")
		}
	}
}

func (t *trace) displayInfo() (d displayInfo) {
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
			name:          strings.Replace(v.Name, "\t", " ", -1),
		}
		if v.Error != "" {
			if strings.HasSuffix(v.Error, context.Canceled.Error()) {
				j.isCanceled = true
				j.name = "CANCELED " + j.name
			} else {
				j.hasError = true
				j.name = "ERROR " + j.name
			}
		}
		if v.Cached {
			j.name = "CACHED " + j.name
		}
		j.name = v.indent + j.name
		d.jobs = append(d.jobs, j)
		for _, s := range v.statuses {
			j := job{
				startTime:     addTime(s.Started, t.localTimeDiff),
				completedTime: addTime(s.Completed, t.localTimeDiff),
				name:          v.indent + "=> " + s.ID,
			}
			if s.Total != 0 {
				j.status = fmt.Sprintf("%.2f / %.2f", units.Bytes(s.Current), units.Bytes(s.Total))
			} else if s.Current != 0 {
				j.status = fmt.Sprintf("%.2f", units.Bytes(s.Current))
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

func (disp *display) print(d displayInfo, all bool) {
	// this output is inspired by Buck
	width := 80
	height := 10
	size, err := disp.c.Size()
	if err == nil && size.Width > 0 && size.Height > 0 {
		width = int(size.Width)
		height = int(size.Height)
	}

	if !all {
		d.jobs = wrapHeight(d.jobs, height-2)
	}

	b := aec.EmptyBuilder
	for i := 0; i <= disp.lineCount; i++ {
		b = b.Up(1)
	}
	if !disp.repeated {
		b = b.Down(1)
	}
	disp.repeated = true
	fmt.Fprint(disp.c, b.Column(0).ANSI)

	statusStr := ""
	if d.countCompleted > 0 && d.countCompleted == d.countTotal && all {
		statusStr = "FINISHED"
	}

	fmt.Fprint(disp.c, aec.Hide)
	defer fmt.Fprint(disp.c, aec.Show)

	out := fmt.Sprintf("[+] Building %.1fs (%d/%d) %s", time.Since(d.startTime).Seconds(), d.countCompleted, d.countTotal, statusStr)
	out = align(out, "", width)
	fmt.Fprintln(disp.c, out)
	lineCount := 0
	for _, j := range d.jobs {
		endTime := time.Now()
		if j.completedTime != nil {
			endTime = *j.completedTime
		}
		if j.startTime == nil {
			continue
		}
		dt := endTime.Sub(*j.startTime).Seconds()
		if dt < 0.05 {
			dt = 0
		}
		pfx := " => "
		timer := fmt.Sprintf(" %3.1fs\n", dt)
		status := j.status
		showStatus := false

		left := width - len(pfx) - len(timer) - 1
		if status != "" {
			if left+len(status) > 20 {
				showStatus = true
				left -= len(status) + 1
			}
		}
		if left < 12 { // too small screen to show progress
			continue
		}
		if len(j.name) > left {
			j.name = j.name[:left]
		}

		out := pfx + j.name
		if showStatus {
			out += " " + status
		}

		out = align(out, timer, width)
		if j.completedTime != nil {
			color := aec.BlueF
			if j.isCanceled {
				color = aec.YellowF
			} else if j.hasError {
				color = aec.RedF
			}
			out = aec.Apply(out, color)
		}
		fmt.Fprint(disp.c, out)
		lineCount++
	}
	disp.lineCount = lineCount
}

func align(l, r string, w int) string {
	return fmt.Sprintf("%-[2]*[1]s %[3]s", l, w-len(r)-1, r)
}

func wrapHeight(j []job, limit int) []job {
	if len(j) > limit {
		j = j[len(j)-limit:]
	}
	return j
}
