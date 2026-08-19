package steamcmd

import (
	"context"
	"time"

	"github.com/hashicorp/nomad/v2/plugins/drivers"
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
		cpuPct, _ := proc.CPUPercent()
		memInfo, _ := proc.MemoryInfo()

		usage := &drivers.TaskResourceUsage{
			ResourceUsage: &drivers.ResourceUsage{
				CpuStats: &drivers.CpuStats{
					Percent:  cpuPct,
					Measured: []string{"Percent"},
				},
				MemoryStats: &drivers.MemoryStats{
					Measured: []string{"RSS"},
				},
			},
			Timestamp: time.Now().UnixNano(),
		}
		if memInfo != nil {
			usage.ResourceUsage.MemoryStats.RSS = memInfo.RSS
		}

		select {
		case ch <- usage:
		case <-ctx.Done():
			return
		}
	}
}
