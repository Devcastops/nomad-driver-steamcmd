package steamcmd

import (
	"os"
	"syscall"
	"testing"

	"github.com/shirou/gopsutil/v3/process"
)

func TestResolveLaunchPath(t *testing.T) {
	cases := []struct {
		name       string
		command    string
		installDir string
		want       string
	}{
		{
			name:       "relative path with directory component resolves against installDir",
			command:    "local/steamapp/PalServer.sh",
			installDir: "/alloc/task/local/steamapp",
			want:       "/alloc/task/local/steamapp/local/steamapp/PalServer.sh",
		},
		{
			name:       "bare command name is left alone for PATH lookup",
			command:    "xvfb-run",
			installDir: "/alloc/task/local/steamapp",
			want:       "xvfb-run",
		},
		{
			name:       "bare command name 'wine' is left alone for PATH lookup",
			command:    "wine",
			installDir: "/alloc/task/local/steamapp",
			want:       "wine",
		},
		{
			name:       "already-absolute path is left alone",
			command:    "/usr/bin/wine",
			installDir: "/alloc/task/local/steamapp",
			want:       "/usr/bin/wine",
		},
		{
			name:       "relative path directly under installDir",
			command:    "PalServer.sh",
			installDir: "/alloc/task/local/steamapp",
			want:       "PalServer.sh", // no "/" in command -> bare name, PATH lookup
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLaunchPath(tc.command, tc.installDir)
			if got != tc.want {
				t.Errorf("resolveLaunchPath(%q, %q) = %q, want %q", tc.command, tc.installDir, got, tc.want)
			}
		})
	}
}

func TestSignalProcessGroup_NonexistentPIDReturnsProcessDone(t *testing.T) {
	// A process group that's already gone (e.g. the task already exited
	// by the time StopTask/DestroyTask gets to it) must be treated as
	// "already done", not as a real failure -- same contract
	// cmd.Process.Signal() had via os.ErrProcessDone before this switched
	// to signaling the whole group via syscall.Kill(-pid, ...).
	const definitelyNotARealPID = 999999999

	err := signalProcessGroup(definitelyNotARealPID, syscall.SIGTERM)
	if err != os.ErrProcessDone {
		t.Errorf("signalProcessGroup on a nonexistent PID = %v, want os.ErrProcessDone", err)
	}
}

func TestSumProcessTree_DoesNotErrorOnCurrentProcess(t *testing.T) {
	// A full multi-process parent/child aggregation test would need to
	// actually spawn and wait on real child processes, which is flaky in
	// a CI sandbox. This is a lighter sanity check: sumProcessTree must
	// run cleanly (no panic) against a real, currently-running process
	// and return non-negative values, proving the recursion terminates
	// and the gopsutil calls are wired correctly even with no children.
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		t.Fatalf("process.NewProcess(self) failed: %v", err)
	}

	cpuPct, rss := sumProcessTree(proc)
	if cpuPct < 0 {
		t.Errorf("sumProcessTree cpuPct = %v, want >= 0", cpuPct)
	}
	if rss == 0 {
		t.Error("sumProcessTree rss = 0 for the current live process, want > 0")
	}
}
