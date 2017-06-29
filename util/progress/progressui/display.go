package progressui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/containerd/console"
	"github.com/docker/go-units"
	"github.com/moby/buildkit/client"
	"github.com/morikuni/aec"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/time/rate"
)

func DisplaySolveStatus(ctx context.Context, ch chan *client.SolveStatus) error {
	c, err := console.ConsoleFromFile(os.Stdout)
	if err != nil {
		return err // TODO: switch to log mode
	}
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
			t.printErrorLogs()
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

func (t *trace) printErrorLogs() {
	for _, v := range t.vertexes {
		if v.Error != "" && !strings.HasSuffix(v.Error, context.Canceled.Error()) {
			fmt.Println("------")
			fmt.Printf(" > %s:\n", v.Name)
			for _, l := range v.logs {
				switch l.Stream {
				case 1:
					os.Stdout.Write(l.Data)
				case 2:
					os.Stderr.Write(l.Data)
				}
			}
			fmt.Println("------")
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
			name:          v.Name,
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
		d.jobs = append(d.jobs, j)
		for _, s := range v.statuses {
			j := job{
				startTime:     addTime(s.Started, t.localTimeDiff),
				completedTime: addTime(s.Completed, t.localTimeDiff),
				name:          "=> " + s.ID,
			}
			if s.Total != 0 {
				j.status = units.HumanSize(float64(s.Current)) + " / " + units.HumanSize(float64(s.Total))
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
	if err == nil {
		width = int(size.Width)
		height = int(size.Height)
	}

	if !all {
		d.jobs = wrapHeight(d.jobs, height-2)
	}

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
		timer := fmt.Sprintf(" %.1fs\n", dt)
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

		out = fmt.Sprintf("%-[2]*[1]s %[3]s", out, width-len(timer)-1, timer)
		if j.completedTime != nil {
			color := aec.BlueF
			if j.isCanceled {
				color = aec.YellowF
			} else if j.hasError {
				color = aec.RedF
			}
			out = aec.Apply(out, color)
		}
		fmt.Print(out)
		lineCount++
	}
	disp.lineCount = lineCount
}

func wrapHeight(j []job, limit int) []job {
	if len(j) > limit {
		j = j[len(j)-limit:]
	}
	return j
}
