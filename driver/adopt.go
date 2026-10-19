package steamcmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/hashicorp/nomad/plugins/drivers"
)

// startLogFollowers streams the follow_logs files into the task's stdout and
// returns a function that stops them (after a final drain), or nil if there
// is nothing to follow. stale is the pre-launch snapshot (see snapshotFiles):
// for a task that is already running it is simply the current state, so
// following resumes with lines written from now on.
//
// The follower opens its own handle on the stdout fifo because StartTask's
// deferred closeLogs() closes the ones it opened when it returns.
func (d *Driver) startLogFollowers(cfg *drivers.TaskConfig, paths []string, stale map[string]os.FileInfo) func() {
	if len(paths) == 0 {
		return nil
	}
	lw, err := os.OpenFile(cfg.StdoutPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		d.logger.Warn("follow_logs disabled: cannot open task stdout", "task_id", cfg.ID, "error", err)
		return nil
	}
	fctx, fcancel := context.WithCancel(context.Background())
	fdone := make(chan struct{})
	go func() {
		defer close(fdone)
		defer lw.Close()
		followLogs(fctx, paths, stale, lw, logPollInterval)
	}()
	d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(),
		Message: fmt.Sprintf("following %d log file(s) into task stdout", len(paths))})
	return func() {
		fcancel()
		select {
		case <-fdone:
		case <-time.After(3 * time.Second):
		}
	}
}

// adoptOrReap runs first in StartTask for tasks that launch a process, before
// any install/update. Nomad calls StartTask when it has no handle for the task,
// for example after a client or plugin restart where it could not recover the
// old one. If the earlier instance is in fact still running (Wine/Proton
// processes outlive their wrapper and the plugin), launching another copy would
// put two servers on one world, and killing the running one costs the players
// their session. So:
//
//   - workload still running  -> bind this task to it (no restart, no downtime);
//   - only helpers left (wineserver/Xvfb), meaning the game is gone -> stop them
//     and start fresh.
//
// It returns a non-nil handle when it adopted a running instance. Instances
// are matched by the marker the driver puts in every process's environment, so
// processes launched by driver versions before the marker existed cannot be
// found here.
func (d *Driver) adoptOrReap(cfg *drivers.TaskConfig, taskCfg TaskConfig, h *taskHandle, installDir string) (*drivers.TaskHandle, error) {
	key := taskKey(cfg.AllocID, cfg.Name)
	marker := taskMarker(key)
	list := func() []int32 { return findMarked("/proc", marker) }

	found := list()
	if len(found) == 0 {
		return nil, nil
	}
	var work []int32
	for _, pid := range found {
		if !supportComms[procComm("/proc", pid)] {
			work = append(work, pid)
		}
	}

	if len(work) == 0 {
		d.logger.Warn("only helper processes remain from an earlier start of this task; cleaning them up", "task_id", cfg.ID, "pids", found)
		d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(),
			Message: fmt.Sprintf("cleaning up %d helper process(es) left from an earlier start", len(found))})
		if !terminateProcesses("/proc", list, staleReapSignal, 10*time.Second, d.logger) {
			return nil, fmt.Errorf("helper processes from an earlier start of this task would not stop")
		}
		return nil, nil
	}

	// Adopt. The lowest pid is the launch wrapper (it started first).
	root := work[0]
	for _, pid := range work {
		if pid < root {
			root = pid
		}
	}
	d.logger.Info("re-adopting the running instance of this task instead of starting another",
		"task_id", cfg.ID, "pid", root, "processes", len(found))
	d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(),
		Message: fmt.Sprintf("re-adopted the already-running instance (%d processes) instead of starting a new copy", len(found))})

	if taskCfg.Platform == "windows" {
		home, _ := os.UserHomeDir()
		if rt, err := resolveWindowsRuntime(taskCfg, d.config, cfg.TaskDir().Dir, home); err == nil {
			h.state.WineCleanup = rt.cleanup()
		}
	}

	proc, _ := os.FindProcess(int(root))
	now := time.Now()
	h.mu.Lock()
	h.cmd = &exec.Cmd{Process: proc}
	h.mu.Unlock()
	h.stateLock.Lock()
	h.state.Pid = int(root)
	h.state.Phase = phaseRunning
	h.state.AppID = taskCfg.AppID
	h.state.InstallDir = installDir
	h.startedAt = now
	h.state.StartedAt = now
	snapshot := h.state
	h.stateLock.Unlock()

	handle := drivers.NewTaskHandle(taskHandleVersion)
	handle.Config = cfg
	if err := handle.SetDriverState(&snapshot); err != nil {
		return nil, fmt.Errorf("failed to set driver state: %w", err)
	}

	paths := resolveFollowPaths(installDir, taskCfg.FollowLogs)
	stop := d.startLogFollowers(cfg, paths, snapshotFiles(paths))
	go d.watchRecovered(h, newUsageCollector(key, int32(root), d.compute), stop)
	return handle, nil
}
