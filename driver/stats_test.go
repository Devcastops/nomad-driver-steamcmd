package steamcmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/nomad/client/lib/cpustats"
)

func TestHasMarker(t *testing.T) {
	m := []byte("STEAMCMD_TASK_ID=abc/server")
	cases := []struct {
		name string
		env  string
		want bool
	}{
		{"present", "A=1\x00STEAMCMD_TASK_ID=abc/server\x00B=2\x00", true},
		{"present, no trailing NUL", "A=1\x00STEAMCMD_TASK_ID=abc/server", true},
		{"only entry", "STEAMCMD_TASK_ID=abc/server\x00", true},
		{"different alloc", "STEAMCMD_TASK_ID=xyz/server\x00", false},
		{"task name that merely starts the same", "STEAMCMD_TASK_ID=abc/server2\x00", false},
		{"value embedded in another variable", "X=STEAMCMD_TASK_ID=abc/server\x00", false},
		{"empty", "", false},
		{"legacy v0.4.3 form (full task id)", "STEAMCMD_TASK_ID=abc/server/deadbeef\x00", true},
		{"legacy form of a different task name", "STEAMCMD_TASK_ID=abc/server2/deadbeef\x00", false},
		{"legacy form of a different alloc", "STEAMCMD_TASK_ID=xyz/server/deadbeef\x00", false},
	}
	for _, tc := range cases {
		if got := hasMarker([]byte(tc.env), m); got != tc.want {
			t.Errorf("%s: hasMarker = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// busy starts a CPU-burning shell in its own session (so it is NOT a child
// of anything we track once reparented) with the given extra env.
func busy(t *testing.T, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "while :; do :; done")
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func TestUsageCollector_FindsReparentedMarkedProcessAndIgnoresOthers(t *testing.T) {
	id := fmt.Sprintf("test-%d", time.Now().UnixNano())
	marked := busy(t, taskMarkerEnv+"="+id)
	other := busy(t, taskMarkerEnv+"=someone-else")
	unmarked := busy(t)

	// root is the test process itself, which is not the parent of the busy
	// loops' session leaders' children relation we care about: the point is
	// that membership comes from the marker, not from the process tree.
	c := newUsageCollector(id, 0, cpustats.Compute{TotalCompute: 4000, NumCores: 4})

	got := c.members()
	if !got[int32(marked.Process.Pid)] {
		t.Errorf("marked process %d not found in members %v", marked.Process.Pid, got)
	}
	if got[int32(other.Process.Pid)] || got[int32(unmarked.Process.Pid)] {
		t.Errorf("members must not include other tasks' or unmarked processes: %v", got)
	}

	c.collect() // first sample only records a baseline
	time.Sleep(700 * time.Millisecond)
	u := c.collect()

	if u.CpuStats.Percent < 20 {
		t.Errorf("CPU percent = %.1f for a busy marked process, want a clearly non-zero current rate", u.CpuStats.Percent)
	}
	if u.CpuStats.Percent > 160 {
		t.Errorf("CPU percent = %.1f, looks like unmarked processes were counted", u.CpuStats.Percent)
	}
	if u.CpuStats.TotalTicks <= 0 {
		t.Errorf("TotalTicks = %v, want > 0 when compute is known", u.CpuStats.TotalTicks)
	}
	if u.MemoryStats.RSS == 0 {
		t.Error("RSS = 0, want > 0")
	}
}

func TestUsageCollector_CPUTracksCurrentRateNotLifetimeAverage(t *testing.T) {
	// gopsutil's CPUPercent() is total CPU time / process age, so a process that
	// burned CPU for a while and then went idle would keep reporting a high
	// number. The delta-based collector must drop back near zero.
	id := fmt.Sprintf("test-%d", time.Now().UnixNano())
	cmd := busy(t, taskMarkerEnv+"="+id)
	c := newUsageCollector(id, 0, cpustats.Compute{})

	c.collect()
	time.Sleep(600 * time.Millisecond)
	if p := c.collect().CpuStats.Percent; p < 20 {
		t.Fatalf("busy phase reported %.1f%%", p)
	}

	cmd.Process.Signal(syscall.SIGSTOP) // now idle
	time.Sleep(100 * time.Millisecond)
	c.collect()
	time.Sleep(600 * time.Millisecond)
	if p := c.collect().CpuStats.Percent; p > 10 {
		t.Errorf("idle phase reported %.1f%%, expected near 0 (lifetime-average bug?)", p)
	}
}

func TestUsageCollector_NewProcessContributesNothingOnFirstSample(t *testing.T) {
	id := fmt.Sprintf("test-%d", time.Now().UnixNano())
	busy(t, taskMarkerEnv+"="+id)
	time.Sleep(300 * time.Millisecond) // let it accumulate CPU time before we ever look
	c := newUsageCollector(id, 0, cpustats.Compute{})
	if p := c.collect().CpuStats.Percent; p != 0 {
		t.Errorf("first sample percent = %.1f, want 0 (no baseline yet)", p)
	}
}
