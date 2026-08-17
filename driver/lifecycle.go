package steamcmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/hashicorp/nomad/plugins/drivers"
)

// minFreeBytes is a conservative floor below which we refuse to start an
// install. steamcmd will happily begin downloading into a near-full
// filesystem and fail confusingly partway through; failing fast here with
// a clear task event beats a mysterious hang.
const minFreeBytes = 512 * 1024 * 1024 // 512MB

// installDirPopulated is a cheap heuristic for "has steamcmd already put
// something here" -- used to decide whether update_on_start=false should
// actually skip the steamcmd pass, versus a genuinely first-ever start
// where skipping would leave nothing to launch.
func installDirPopulated(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// taskHandleVersion is passed to drivers.NewTaskHandle so Nomad can detect
// (and this driver could, in principle, special-case) handles created by
// an older/incompatible version of this driver during recovery. Bump this
// if taskState's persisted shape changes in a breaking way.
const taskHandleVersion = 1

func (d *Driver) RecoverTask(handle *drivers.TaskHandle) error {
	if handle == nil {
		return fmt.Errorf("handle is nil")
	}
	if _, ok := d.tasks.Load(handle.Config.ID); ok {
		return nil // already tracked, nothing to do
	}

	var state taskState
	if err := handle.GetDriverState(&state); err != nil {
		return fmt.Errorf("failed to decode driver task state: %w", err)
	}

	h := newTaskHandle(d.logger, handle.Config)
	h.state = state

	// StartTask now only returns (and only then does Nomad persist a
	// handle at all) once install has completed and, if configured, the
	// launch process has actually started. So the only state we can ever
	// be recovering into is "a launched process should still be alive" or
	// "this was an install-only task that already finished" -- there is
	// no more "recover mid-install" case, because a driver/client crash
	// during that window means Nomad never received a handle to persist
	// in the first place, and will simply call StartTask again from
	// scratch for that task, same as any other driver.
	switch state.Phase {
	case phaseRunning:
		if state.Pid == 0 {
			return fmt.Errorf("cannot recover running task %s: no PID recorded", handle.Config.ID)
		}
		proc, err := os.FindProcess(state.Pid)
		if err != nil {
			h.setPhase(phaseExited)
			d.tasks.Store(handle.Config.ID, h)
			return nil
		}
		// FindProcess always succeeds on unix; confirm liveness explicitly.
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			d.logger.Info("recovered task process is no longer alive", "task_id", handle.Config.ID, "pid", state.Pid)
			h.setPhase(phaseExited)
			h.stateLock.Lock()
			h.procState = drivers.TaskStateExited
			h.exitResult = &drivers.ExitResult{ExitCode: -1, Err: fmt.Errorf("process not found on recovery")}
			h.stateLock.Unlock()
			d.tasks.Store(handle.Config.ID, h)
			return nil
		}
		h.cmd = &exec.Cmd{Process: proc}
		h.startedAt = state.StartedAt
		d.tasks.Store(handle.Config.ID, h)
		go d.waitOnRecoveredProcess(h, proc)
		return nil

	default:
		h.setPhase(phaseExited)
		d.tasks.Store(handle.Config.ID, h)
		return nil
	}
}

func (d *Driver) StartTask(cfg *drivers.TaskConfig) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	if _, ok := d.tasks.Load(cfg.ID); ok {
		return nil, nil, fmt.Errorf("task with ID %q already started", cfg.ID)
	}

	var taskCfg TaskConfig
	if err := cfg.DecodeDriverConfig(&taskCfg); err != nil {
		return nil, nil, fmt.Errorf("failed to decode task config: %w", err)
	}
	if taskCfg.InstallDir == "" {
		taskCfg.InstallDir = "local/steamapp"
	}

	installDir := taskCfg.InstallDir
	if !filepath.IsAbs(installDir) {
		installDir = filepath.Join(cfg.TaskDir().Dir, installDir)
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("failed to create install_dir %q: %w", installDir, err)
	}

	if free, err := statfsFree(installDir); err == nil && free < minFreeBytes {
		return nil, nil, fmt.Errorf("insufficient disk space for install_dir %q: %d bytes free, need at least %d",
			installDir, free, minFreeBytes)
	}

	login := resolveLogin(taskCfg.Login, d.config.DefaultLogin)

	// Diagnostic: two consecutive jobs in the same driver process and same
	// client config have shown different login outcomes despite neither
	// specifying a task-level login block, which none of the obvious
	// explanations (plugin config not loading, a stray login block, a
	// nil-vs-zero-value LoginConfig pointer) account for. Logging the raw
	// decoded state here, for every task, will show ground truth on the
	// next run instead of another guess.
	taskLoginAnon := false
	if taskCfg.Login != nil {
		taskLoginAnon = taskCfg.Login.Anonymous
	}
	d.logger.Info("steamcmd task config decoded",
		"task_id", cfg.ID,
		"app_id", taskCfg.AppID,
		"task_login_is_nil", taskCfg.Login == nil,
		"task_login_anonymous", taskLoginAnon,
		"launch_is_nil", taskCfg.Launch == nil,
		"plugin_default_login_anonymous", d.config.DefaultLogin.Anonymous,
		"resolved_login_anonymous", login.Anonymous,
	)

	h := newTaskHandle(d.logger, cfg)
	// Tracked immediately so InspectTask/TaskEvents work for the duration
	// of the (potentially long) synchronous install below, even though no
	// *drivers.TaskHandle has been returned to Nomad yet. If the driver or
	// client is killed during this window, Nomad never received a handle
	// and will simply call StartTask again on restart -- same as any
	// driver whose StartTask is interrupted before returning.
	d.tasks.Store(cfg.ID, h)

	stdout, stderr, closeLogs, err := taskLogFiles(cfg)
	if err != nil {
		d.tasks.Delete(cfg.ID)
		return nil, nil, fmt.Errorf("failed to open task log files: %w", err)
	}
	defer closeLogs()

	// If update_on_start is false and this looks like a re-launch of an
	// already-installed app (a launch phase is configured and installDir
	// already has content), skip re-invoking steamcmd entirely rather than
	// silently updating on every restart regardless of the flag.
	skipInstall := !taskCfg.UpdateOnStart && taskCfg.Launch != nil && installDirPopulated(installDir)

	if skipInstall {
		d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(),
			Message: "update_on_start is false and install_dir is already populated; skipping steamcmd"})
	} else {
		args, err := buildArgs(taskCfg, login, installDir)
		if err != nil {
			d.tasks.Delete(cfg.ID)
			return nil, nil, err
		}

		installCtx := h.ctx
		if taskCfg.InstallTimeout != "" {
			if dur, perr := time.ParseDuration(taskCfg.InstallTimeout); perr == nil {
				var cancel context.CancelFunc
				installCtx, cancel = context.WithTimeout(h.ctx, dur)
				defer cancel()
			}
		}

		release := d.acquireInstallSlot(installCtx)
		d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(), Message: "starting steamcmd install/update"})

		// This blocks StartTask for as long as the download takes -- the
		// same tradeoff the Docker driver makes for image pulls. Progress
		// is visible two ways in the meantime: raw steamcmd output goes to
		// the task's log files (`nomad alloc logs`) immediately since
		// stdout/stderr are wired directly, and a throttled human-readable
		// summary is emitted as TaskEvents (`nomad alloc status`) via the
		// onProgress callback below.
		res, err := runInstall(installCtx, d.config.SteamCmdPath, args, stdout, stderr, func(msg string) {
			d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(), Message: msg})
		})
		release()
		if err != nil {
			d.tasks.Delete(cfg.ID)
			d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(), Message: err.Error()})
			return nil, nil, err
		}

		d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(), Message: fmt.Sprintf("install complete: %s", res.Message)})
	}

	handle := drivers.NewTaskHandle(taskHandleVersion)
	handle.Config = cfg

	if taskCfg.Launch == nil {
		// Install-only task: it's already done. Persist an "exited,
		// success" handle and return -- Nomad will see this task complete
		// essentially immediately after StartTask returns.
		h.stateLock.Lock()
		h.procState = drivers.TaskStateExited
		h.completedAt = time.Now()
		h.exitResult = &drivers.ExitResult{ExitCode: 0}
		h.stateLock.Unlock()
		h.setPhase(phaseExited)
		h.state.AppID = taskCfg.AppID
		h.state.InstallDir = installDir

		if err := handle.SetDriverState(&h.state); err != nil {
			d.tasks.Delete(cfg.ID)
			return nil, nil, fmt.Errorf("failed to set driver state: %w", err)
		}
		close(h.doneCh)
		return handle, nil, nil
	}

	launchCmd := exec.CommandContext(h.ctx, taskCfg.Launch.Command, taskCfg.Launch.Args...)
	launchCmd.Dir = installDir
	launchCmd.Stdout = stdout
	launchCmd.Stderr = stderr
	launchCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	env := os.Environ()
	for k, v := range taskCfg.Launch.Env {
		env = append(env, k+"="+v)
	}
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	launchCmd.Env = env

	if err := launchCmd.Start(); err != nil {
		d.tasks.Delete(cfg.ID)
		return nil, nil, fmt.Errorf("failed to launch %q: %w", taskCfg.Launch.Command, err)
	}

	h.mu.Lock()
	h.cmd = launchCmd
	h.mu.Unlock()

	h.stateLock.Lock()
	h.state.Pid = launchCmd.Process.Pid
	h.state.Phase = phaseRunning
	h.state.AppID = taskCfg.AppID
	h.state.InstallDir = installDir
	h.startedAt = time.Now()
	h.state.StartedAt = h.startedAt
	stateSnapshot := h.state
	h.stateLock.Unlock()

	// Now that we know the real PID and phase, persist it. This is the
	// whole point of making StartTask synchronous: RecoverTask will only
	// ever be handed a handle that reflects an actually-running process,
	// never a stale "installing" snapshot from before the launch happened.
	if err := handle.SetDriverState(&stateSnapshot); err != nil {
		_ = launchCmd.Process.Kill()
		d.tasks.Delete(cfg.ID)
		return nil, nil, fmt.Errorf("failed to set driver state: %w", err)
	}

	d.eventer.EmitEvent(&drivers.TaskEvent{TaskID: cfg.ID, TaskName: cfg.Name, Timestamp: time.Now(),
		Message: fmt.Sprintf("launched %s (pid %d)", taskCfg.Launch.Command, launchCmd.Process.Pid)})

	// The launched process is long-running by design (a game server);
	// StartTask must not block on it. Supervise it in the background --
	// closeLogs is deferred here rather than in the outer StartTask defer
	// so the log files stay open for the lifetime of the process.
	go func() {
		defer closeLogs()
		waitErr := launchCmd.Wait()
		h.stateLock.Lock()
		h.completedAt = time.Now()
		h.procState = drivers.TaskStateExited
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				h.exitResult = &drivers.ExitResult{ExitCode: exitErr.ExitCode()}
			} else {
				h.exitResult = &drivers.ExitResult{ExitCode: -1, Err: waitErr}
			}
		} else {
			h.exitResult = &drivers.ExitResult{ExitCode: 0}
		}
		h.stateLock.Unlock()
		h.setPhase(phaseExited)
		close(h.doneCh)
	}()

	return handle, nil, nil
}

func (d *Driver) waitOnRecoveredProcess(h *taskHandle, proc *os.Process) {
	ps, err := proc.Wait()
	h.stateLock.Lock()
	defer h.stateLock.Unlock()
	h.completedAt = time.Now()
	h.procState = drivers.TaskStateExited
	if err != nil {
		h.exitResult = &drivers.ExitResult{ExitCode: -1, Err: err}
	} else {
		h.exitResult = &drivers.ExitResult{ExitCode: ps.ExitCode()}
	}
	close(h.doneCh)
}

func (d *Driver) WaitTask(ctx context.Context, taskID string) (<-chan *drivers.ExitResult, error) {
	h, ok := d.tasks.Load(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}
	handle := h.(*taskHandle)
	ch := make(chan *drivers.ExitResult)
	go func() {
		defer close(ch)
		select {
		case <-handle.doneCh:
			handle.stateLock.Lock()
			res := handle.exitResult
			handle.stateLock.Unlock()
			select {
			case ch <- res:
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// StopTask sends SIGTERM (or the configured kill signal) and gives the
// process kill_timeout to exit gracefully -- important for game servers
// that need time to flush saves -- before escalating to SIGKILL.
func (d *Driver) StopTask(taskID string, timeout time.Duration, signal string) error {
	v, ok := d.tasks.Load(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}
	h := v.(*taskHandle)

	h.mu.RLock()
	cmd := h.cmd
	h.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return nil // install-only task with nothing running, or already exited
	}

	sig := syscall.SIGTERM
	if signal != "" {
		if s, err := parseSignal(signal); err == nil {
			sig = s
		}
	}

	if err := cmd.Process.Signal(sig); err != nil && err != os.ErrProcessDone {
		d.logger.Warn("failed to send stop signal, will escalate to SIGKILL", "task_id", taskID, "error", err)
	}

	select {
	case <-h.doneCh:
		return nil
	case <-time.After(timeout):
		d.logger.Warn("task did not exit within kill_timeout, sending SIGKILL", "task_id", taskID, "timeout", timeout)
		_ = cmd.Process.Kill()
		return nil
	}
}

func (d *Driver) DestroyTask(taskID string, force bool) error {
	v, ok := d.tasks.Load(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}
	h := v.(*taskHandle)

	if h.IsRunning() {
		if !force {
			return fmt.Errorf("cannot destroy running task %s without force", taskID)
		}
		h.mu.RLock()
		cmd := h.cmd
		h.mu.RUnlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	h.cancel()
	d.tasks.Delete(taskID)
	return nil
}

func (d *Driver) InspectTask(taskID string) (*drivers.TaskStatus, error) {
	v, ok := d.tasks.Load(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}
	return v.(*taskHandle).TaskStatus(), nil
}

func (d *Driver) TaskStats(ctx context.Context, taskID string, interval time.Duration) (<-chan *drivers.TaskResourceUsage, error) {
	v, ok := d.tasks.Load(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}
	h := v.(*taskHandle)
	ch := make(chan *drivers.TaskResourceUsage)
	go pollStats(ctx, h, interval, ch)
	return ch, nil
}

func (d *Driver) TaskEvents(ctx context.Context) (<-chan *drivers.TaskEvent, error) {
	return d.eventer.TaskEvents(ctx)
}

func (d *Driver) SignalTask(taskID string, signal string) error {
	v, ok := d.tasks.Load(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}
	h := v.(*taskHandle)
	h.mu.RLock()
	cmd := h.cmd
	h.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("task %s has no running process to signal", taskID)
	}
	sig, err := parseSignal(signal)
	if err != nil {
		return err
	}
	return cmd.Process.Signal(sig)
}

func (d *Driver) ExecTask(taskID string, cmdArgs []string, timeout time.Duration) (*drivers.ExecTaskResult, error) {
	return nil, fmt.Errorf("steamcmd driver does not support exec")
}

func taskLogFiles(cfg *drivers.TaskConfig) (stdout, stderr *os.File, closeFn func(), err error) {
	so, err := os.OpenFile(cfg.StdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, nil, err
	}
	se, err := os.OpenFile(cfg.StderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		so.Close()
		return nil, nil, nil, err
	}
	return so, se, func() { so.Close(); se.Close() }, nil
}

func parseSignal(s string) (syscall.Signal, error) {
	switch s {
	case "SIGTERM", "TERM":
		return syscall.SIGTERM, nil
	case "SIGKILL", "KILL":
		return syscall.SIGKILL, nil
	case "SIGINT", "INT":
		return syscall.SIGINT, nil
	case "SIGHUP", "HUP":
		return syscall.SIGHUP, nil
	case "SIGUSR1", "USR1":
		return syscall.SIGUSR1, nil
	case "SIGUSR2", "USR2":
		return syscall.SIGUSR2, nil
	default:
		if n, err := strconv.Atoi(s); err == nil {
			return syscall.Signal(n), nil
		}
		return 0, fmt.Errorf("unsupported signal: %s", s)
	}
}
