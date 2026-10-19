package steamcmd

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
)

// supportComms are helper processes the workload depends on. They must be
// stopped last: killing wineserver or Xvfb first hard-kills the game before
// it can save.
var supportComms = map[string]bool{"wineserver": true, "Xvfb": true}

// staleReapSignal is the graceful signal used to stop leftovers from an
// earlier start. SIGINT, because Wine maps it to Ctrl-C (a clean shutdown for
// many servers), whereas SIGTERM tends to be a hard kill under Wine.
const staleReapSignal = syscall.SIGINT

func taskMarker(key string) []byte {
	return []byte(taskMarkerEnv + "=" + key)
}

// procAlive reports whether pid exists and is not a zombie.
func procAlive(procDir string, pid int32) bool {
	b, err := os.ReadFile(procDir + "/" + strconv.Itoa(int(pid)) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	switch s[i+2] {
	case 'Z', 'X':
		return false
	}
	return true
}

func procComm(procDir string, pid int32) string {
	b, err := os.ReadFile(procDir + "/" + strconv.Itoa(int(pid)) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// findMarked returns the live pids (excluding this process) whose environment
// contains marker. It reads every process's environ, so it is meant for the
// rare paths (start, stop, recover), not for per-second polling.
func findMarked(procDir string, marker []byte) []int32 {
	c := &usageCollector{procDir: procDir}
	self := int32(os.Getpid())
	var out []int32
	for _, pid := range c.listPIDs() {
		if pid == self || !procAlive(procDir, pid) {
			continue
		}
		env, err := os.ReadFile(procDir + "/" + strconv.Itoa(int(pid)) + "/environ")
		if err == nil && hasMarker(env, marker) {
			out = append(out, pid)
		}
	}
	return out
}

func signalPIDs(pids []int32, sig syscall.Signal) {
	for _, pid := range pids {
		_ = syscall.Kill(int(pid), sig)
	}
}

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// terminateProcesses stops a task's processes in dependency order, reporting
// whether everything is gone:
//
//  1. sig to the workload (everything except wineserver/Xvfb), waiting up to
//     grace for it to exit on its own;
//  2. SIGKILL for any workload process still alive;
//  3. SIGTERM, then SIGKILL, for the support processes.
//
// list is re-evaluated each time, so processes spawned meanwhile are caught.
func terminateProcesses(procDir string, list func() []int32, sig syscall.Signal, grace time.Duration, logger hclog.Logger) bool {
	split := func() (work, support []int32) {
		for _, pid := range list() {
			if !procAlive(procDir, pid) {
				continue
			}
			if supportComms[procComm(procDir, pid)] {
				support = append(support, pid)
			} else {
				work = append(work, pid)
			}
		}
		return
	}
	noWork := func() bool { w, _ := split(); return len(w) == 0 }
	noSupport := func() bool { _, s := split(); return len(s) == 0 }

	if work, _ := split(); len(work) > 0 {
		signalPIDs(work, sig)
		if !waitUntil(grace, noWork) {
			work, _ = split()
			if len(work) > 0 {
				logger.Warn("task processes did not exit within the grace period, sending SIGKILL", "pids", work, "grace", grace)
				signalPIDs(work, syscall.SIGKILL)
				waitUntil(5*time.Second, noWork)
			}
		}
	}
	if _, support := split(); len(support) > 0 {
		signalPIDs(support, syscall.SIGTERM)
		if !waitUntil(5*time.Second, noSupport) {
			_, support = split()
			signalPIDs(support, syscall.SIGKILL)
			waitUntil(5*time.Second, noSupport)
		}
	}
	w, s := split()
	return len(w) == 0 && len(s) == 0
}

// liveMembers is members() as a slice, without zombies.
func (c *usageCollector) liveMembers() []int32 {
	var out []int32
	for pid := range c.members() {
		if procAlive(c.procDir, pid) {
			out = append(out, pid)
		}
	}
	return out
}

// waitForExit blocks until the task has no live processes (true) or ctx is
// cancelled first (false).
func waitForExit(ctx context.Context, c *usageCollector, interval time.Duration) bool {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if len(c.liveMembers()) == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}
