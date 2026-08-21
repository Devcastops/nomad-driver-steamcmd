package steamcmd

import (
	"context"
	"time"

	"github.com/hashicorp/nomad/plugins/drivers"
	"github.com/shirou/gopsutil/v3/process"
)

// pollStats reports basic CPU/memory usage for the launched process by
// reading /proc via gopsutil. This piggybacks on gopsutil the same way the
// built-in exec driver does, rather than reimplementing /proc parsing.
func pollStats(ctx context.Context, h *taskHandle, interval time.Duration, ch chan<- *drivers.TaskResourceUsage) {
	defer close(ch)
	if interval <= 0 {
		interval = 1 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

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

		proc, err := process.NewProcess(int32(cmd.Process.Pid))
		if err != nil {
			continue
		}
		cpuPct, rss := sumProcessTree(proc)

		usage := &drivers.TaskResourceUsage{
			ResourceUsage: &drivers.ResourceUsage{
				CpuStats: &drivers.CpuStats{
					Percent:  cpuPct,
					Measured: []string{"Percent"},
				},
				MemoryStats: &drivers.MemoryStats{
					RSS:      rss,
					Measured: []string{"RSS"},
				},
			},
			Timestamp: time.Now().UnixNano(),
		}

		select {
		case ch <- usage:
		case <-ctx.Done():
			return
		}
	}
}

// sumProcessTree aggregates CPU% and RSS across proc and all of its
// descendants, not just proc itself.
//
// A plain process.NewProcess(pid).CPUPercent()/MemoryInfo() only reports
// the single top-level process. That's wrong for the launch pattern this
// driver is specifically designed to support: a wrapper script
// (xvfb-run, or any shell wrapper) that spawns real child processes
// (Xvfb, wine, the actual game server) without exec-replacing itself.
// Reporting only the wrapper's own usage is misleadingly close to zero
// regardless of how much work the real workload underneath it is doing --
// confirmed in production against exactly this pattern (a task showing
// ~2MB RSS / 0% CPU while a multi-hundred-MB Wine process tree was
// legitimately running underneath the xvfb-run wrapper).
func sumProcessTree(root *process.Process) (cpuPct float64, rss uint64) {
	if pct, err := root.CPUPercent(); err == nil {
		cpuPct += pct
	}
	if mem, err := root.MemoryInfo(); err == nil && mem != nil {
		rss += mem.RSS
	}

	children, err := root.Children()
	if err != nil {
		// gopsutil returns an error here when a process simply has no
		// children, not just on a real failure -- nothing more to add.
		return cpuPct, rss
	}
	for _, child := range children {
		childCPU, childRSS := sumProcessTree(child)
		cpuPct += childCPU
		rss += childRSS
	}
	return cpuPct, rss
}
