package steamcmd

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"time"

	"github.com/hashicorp/nomad/client/lib/cpustats"
	"github.com/hashicorp/nomad/plugins/drivers"
	"github.com/shirou/gopsutil/v3/process"
)

// taskMarkerEnv is set (to taskKey: "<alloc id>/<task name>") in the
// environment of every process this driver launches. Children inherit it, so
// it identifies the whole workload even after processes leave the launched
// process's tree, and it stays the same across restarts of the task, which is
// what lets a later start (or a restarted plugin) find an earlier instance.
//
// That matters here: Wine/Proton reparent the real game and wineserver to
// init (ppid 1), so walking children of the launched wrapper (xvfb-run) finds
// almost nothing and the task would show ~0 CPU and a few MB of memory.
const taskMarkerEnv = "STEAMCMD_TASK_ID"

// nonMemberRecheck bounds how long a "not ours" verdict is cached, so a
// recycled pid is eventually re-examined without re-reading every process's
// environment on every tick.
const nonMemberRecheck = 60 * time.Second

type cpuSample struct {
	user, sys float64 // cumulative CPU seconds
	at        time.Time
}

type memberState struct {
	member  bool
	checked time.Time
}

// usageCollector turns repeated samples of a task's processes into Nomad
// resource usage.
type usageCollector struct {
	marker  []byte // "STEAMCMD_TASK_ID=<id>"
	rootPid int32
	compute cpustats.Compute

	procDir string
	now     func() time.Time

	prev  map[int32]cpuSample
	known map[int32]memberState
}

// taskKey identifies a task across restarts and plugin restarts.
func taskKey(allocID, taskName string) string { return allocID + "/" + taskName }

func newUsageCollector(key string, rootPid int32, compute cpustats.Compute) *usageCollector {
	return &usageCollector{
		marker:  taskMarker(key),
		rootPid: rootPid,
		compute: compute,
		procDir: "/proc",
		now:     time.Now,
		prev:    make(map[int32]cpuSample),
		known:   make(map[int32]memberState),
	}
}

// hasMarker reports whether a /proc/<pid>/environ blob (NUL-separated
// KEY=VALUE entries) contains exactly marker as one of its entries.
func hasMarker(environ, marker []byte) bool {
	for len(environ) > 0 {
		end := bytes.IndexByte(environ, 0)
		var entry []byte
		if end < 0 {
			entry, environ = environ, nil
		} else {
			entry, environ = environ[:end], environ[end+1:]
		}
		if bytes.Equal(entry, marker) {
			return true
		}
	}
	return false
}

func (c *usageCollector) listPIDs() []int32 {
	entries, err := os.ReadDir(c.procDir)
	if err != nil {
		return nil
	}
	pids := make([]int32, 0, len(entries))
	for _, e := range entries {
		if n, err := strconv.ParseInt(e.Name(), 10, 32); err == nil && n > 0 {
			pids = append(pids, int32(n))
		}
	}
	return pids
}

// members returns every pid belonging to the task: processes carrying the
// task marker plus the launched process's descendants (a fallback for
// processes started before the marker existed, e.g. recovered tasks, or when
// /proc/<pid>/environ is unreadable).
func (c *usageCollector) members() map[int32]bool {
	now := c.now()
	out := make(map[int32]bool)

	alive := make(map[int32]bool)
	for _, pid := range c.listPIDs() {
		alive[pid] = true
		st, seen := c.known[pid]
		if seen && (st.member || now.Sub(st.checked) < nonMemberRecheck) {
			if st.member {
				out[pid] = true
			}
			continue
		}
		env, err := os.ReadFile(c.procDir + "/" + strconv.Itoa(int(pid)) + "/environ")
		isMember := err == nil && hasMarker(env, c.marker)
		c.known[pid] = memberState{member: isMember, checked: now}
		if isMember {
			out[pid] = true
		}
	}
	for pid := range c.known {
		if !alive[pid] {
			delete(c.known, pid)
		}
	}

	if c.rootPid > 0 {
		if root, err := process.NewProcess(c.rootPid); err == nil {
			addTree(root, out)
		}
	}
	return out
}

func addTree(p *process.Process, out map[int32]bool) {
	out[p.Pid] = true
	children, err := p.Children()
	if err != nil {
		return // gopsutil errors when a process simply has no children
	}
	for _, ch := range children {
		addTree(ch, out)
	}
}

// collect samples every task process once.
func (c *usageCollector) collect() *drivers.ResourceUsage {
	now := c.now()
	var userPct, sysPct float64
	var rss uint64

	cur := make(map[int32]cpuSample)
	for pid := range c.members() {
		p, err := process.NewProcess(pid)
		if err != nil {
			continue
		}
		if t, err := p.Times(); err == nil {
			s := cpuSample{user: t.User, sys: t.System, at: now}
			cur[pid] = s
			// A pid with no previous sample (just appeared) contributes
			// nothing this round rather than its whole lifetime average.
			if prev, ok := c.prev[pid]; ok {
				if dt := s.at.Sub(prev.at).Seconds(); dt > 0 {
					if d := s.user - prev.user; d > 0 {
						userPct += d / dt * 100
					}
					if d := s.sys - prev.sys; d > 0 {
						sysPct += d / dt * 100
					}
				}
			}
		}
		if mem, err := p.MemoryInfo(); err == nil && mem != nil {
			rss += mem.RSS
		}
	}
	c.prev = cur

	total := userPct + sysPct
	cpu := &drivers.CpuStats{
		Percent:    total,
		UserMode:   userPct,
		SystemMode: sysPct,
		Measured:   []string{"System Mode", "User Mode", "Percent"},
	}
	if c.compute.NumCores > 0 {
		cpu.TotalTicks = total / 100 * float64(c.compute.TotalCompute) / float64(c.compute.NumCores)
	}
	return &drivers.ResourceUsage{
		CpuStats: cpu,
		MemoryStats: &drivers.MemoryStats{
			RSS:      rss,
			Measured: []string{"RSS"},
		},
	}
}

// pollStats reports CPU/memory for everything the task is running. CPU is
// computed from the change in CPU time between samples, as a percentage of
// one core (so it can exceed 100), which is what Nomad's own drivers report.
// RSS is summed across processes, so pages shared between Wine processes are
// counted once per process: a mild over-estimate.
func pollStats(ctx context.Context, h *taskHandle, interval time.Duration, compute cpustats.Compute, ch chan<- *drivers.TaskResourceUsage) {
	defer close(ch)
	if interval <= 0 {
		interval = 1 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var c *usageCollector
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.doneCh:
			return
		case <-ticker.C:
		}

		h.mu.RLock()
		cmd := h.cmd
		h.mu.RUnlock()
		if cmd == nil || cmd.Process == nil {
			continue
		}
		if c == nil {
			c = newUsageCollector(taskKey(h.taskConfig.AllocID, h.taskConfig.Name), int32(cmd.Process.Pid), compute)
		}

		usage := &drivers.TaskResourceUsage{
			ResourceUsage: c.collect(),
			Timestamp:     time.Now().UnixNano(),
		}
		select {
		case ch <- usage:
		case <-ctx.Done():
			return
		}
	}
}
