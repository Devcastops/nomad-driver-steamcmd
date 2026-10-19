package steamcmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/client/lib/cpustats"
)

func uniqueID(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

// startShell runs script in its own session with extra env and reaps it when
// the test ends. The returned channel closes when the process has been waited
// on (so tests can tell "dead" from "zombie").
func startShell(t *testing.T, script string, env ...string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	return cmd, done
}

// startNamed runs `sleep 60` under the given comm name (via a symlink), e.g.
// "wineserver", so supportComms matching can be exercised.
func startNamed(t *testing.T, name string, env ...string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	link := filepath.Join(t.TempDir(), name)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	if err := os.Symlink(sleep, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(link, "60")
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	return cmd, done
}

func isDone(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestFindMarked(t *testing.T) {
	id := uniqueID("find")
	a, _ := startShell(t, "sleep 30", taskMarkerEnv+"="+id+"/server")
	startShell(t, "sleep 30", taskMarkerEnv+"="+id+"/other-task")
	startShell(t, "sleep 30")

	got := findMarked("/proc", taskMarker(id+"/server"))
	if len(got) < 1 {
		t.Fatalf("no marked process found")
	}
	found := false
	for _, p := range got {
		if p == int32(a.Process.Pid) {
			found = true
		}
	}
	if !found {
		t.Errorf("pid %d not in %v", a.Process.Pid, got)
	}
	for _, p := range got {
		if !procAlive("/proc", p) {
			t.Errorf("zombie/dead pid %d returned", p)
		}
	}
	if len(findMarked("/proc", taskMarker(id+"/nonexistent"))) != 0 {
		t.Error("matched the wrong task")
	}
}

func TestTerminateProcesses_StopsWorkloadBeforeSupport(t *testing.T) {
	id := uniqueID("order")
	m := taskMarkerEnv + "=" + id
	// Workload takes ~1s to shut down after SIGINT, like a game saving.
	work, workDone := startShell(t, `trap 'sleep 1; exit 0' INT; while :; do :; done`, m)
	supp, suppDone := startNamed(t, "wineserver", m)
	time.Sleep(200 * time.Millisecond) // let the shell install its trap

	c := newUsageCollector(id, 0, cpustats.Compute{})
	res := make(chan bool, 1)
	go func() {
		res <- terminateProcesses("/proc", c.liveMembers, syscall.SIGINT, 10*time.Second, hclog.NewNullLogger())
	}()

	time.Sleep(500 * time.Millisecond)
	if isDone(workDone) {
		t.Fatal("workload exited too early to observe ordering (test timing)")
	}
	if isDone(suppDone) {
		t.Errorf("wineserver (pid %d) was stopped while the workload (pid %d) was still shutting down", supp.Process.Pid, work.Process.Pid)
	}
	select {
	case ok := <-res:
		if !ok {
			t.Error("terminateProcesses reported survivors")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("terminateProcesses did not return")
	}
	if !isDone(workDone) || !isDone(suppDone) {
		t.Errorf("workload done=%v support done=%v, want both", isDone(workDone), isDone(suppDone))
	}
}

func TestTerminateProcesses_EscalatesToKill(t *testing.T) {
	id := uniqueID("kill")
	_, done := startShell(t, `trap '' INT TERM; while :; do :; done`, taskMarkerEnv+"="+id)
	time.Sleep(200 * time.Millisecond)

	c := newUsageCollector(id, 0, cpustats.Compute{})
	start := time.Now()
	ok := terminateProcesses("/proc", c.liveMembers, syscall.SIGINT, 400*time.Millisecond, hclog.NewNullLogger())
	if !ok || !isDone(done) {
		t.Fatalf("ok=%v done=%v: process ignoring the signal should have been SIGKILLed", ok, isDone(done))
	}
	if time.Since(start) > 8*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestTerminateProcesses_LeavesOtherTasksAlone(t *testing.T) {
	id := uniqueID("mine")
	_, mine := startShell(t, "sleep 30", taskMarkerEnv+"="+id)
	_, other := startShell(t, "sleep 30", taskMarkerEnv+"="+uniqueID("theirs"))
	_, none := startShell(t, "sleep 30")

	c := newUsageCollector(id, 0, cpustats.Compute{})
	terminateProcesses("/proc", c.liveMembers, syscall.SIGTERM, 2*time.Second, hclog.NewNullLogger())
	if !isDone(mine) {
		t.Error("own process still running")
	}
	if isDone(other) || isDone(none) {
		t.Error("another task's / unmarked process was killed")
	}
}

func TestWaitForExit(t *testing.T) {
	id := uniqueID("wait")
	cmd, done := startShell(t, "exec sleep 30", taskMarkerEnv+"="+id)
	c := newUsageCollector(id, 0, cpustats.Compute{})

	res := make(chan bool, 1)
	go func() { res <- waitForExit(context.Background(), c, 50*time.Millisecond) }()
	select {
	case <-res:
		t.Fatal("reported exit while the process is still running")
	case <-time.After(300 * time.Millisecond):
	}
	cmd.Process.Kill()
	<-done
	select {
	case ok := <-res:
		if !ok {
			t.Error("want true once the process is gone")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not notice the process exiting")
	}
}

func TestWaitForExit_Cancelled(t *testing.T) {
	id := uniqueID("cancel")
	startShell(t, "sleep 30", taskMarkerEnv+"="+id)
	c := newUsageCollector(id, 0, cpustats.Compute{})
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan bool, 1)
	go func() { res <- waitForExit(ctx, c, 50*time.Millisecond) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case ok := <-res:
		if ok {
			t.Error("want false when cancelled while the process is alive")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after cancel")
	}
}

func TestProcAlive_ZombieIsNotAlive(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Exited but not yet waited on: a zombie.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && procAlive("/proc", int32(cmd.Process.Pid)) {
		time.Sleep(20 * time.Millisecond)
	}
	if procAlive("/proc", int32(cmd.Process.Pid)) {
		t.Error("zombie reported as alive")
	}
	cmd.Wait()
}
