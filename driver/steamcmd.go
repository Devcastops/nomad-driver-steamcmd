package steamcmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// steamcmd is well known for returning exit code 0 even on partial or
// failed installs, and occasionally nonzero on states that aren't real
// failures (e.g. some versions on "already up to date"). We do not trust
// the exit code alone -- we scan stdout for known terminal markers.
var (
	reSuccess  = regexp.MustCompile(`Success!\s*App\s*'(\d+)'\s*(fully installed|fully updated|already up to date)`)
	reError    = regexp.MustCompile(`(?i)ERROR!\s*(.+)`)
	reAuthErr  = regexp.MustCompile(`(?i)(FAILED|Invalid Password|Two-factor|Login Failure|Rate Limit Exceeded)`)
	reProgress = regexp.MustCompile(`Update state \(0x[0-9a-fA-F]+\)\s+([a-zA-Z ,]+?),\s*progress:\s*([\d.]+)`)
)

// progressThrottle is the minimum interval between progress TaskEvents.
// steamcmd emits a progress line on nearly every line of output during a
// download; emitting a Nomad TaskEvent for each would flood and evict the
// (small, capped) task event history Nomad keeps. This mirrors how the
// Docker driver throttles image-pull layer progress events.
const progressThrottle = 5 * time.Second

// installResult is the outcome of a steamcmd install/update invocation,
// derived from parsing stdout rather than trusting the exit code alone.
type installResult struct {
	Success    bool
	AuthFailed bool
	ExitCode   int
	Message    string
}

// buildArgs constructs the steamcmd argument list for an install/update.
// login must already be resolved (task override or plugin default).
func buildArgs(cfg TaskConfig, login LoginConfig, installDir string) ([]string, error) {
	var args []string

	args = append(args, "+force_install_dir", installDir)

	switch {
	case login.Anonymous:
		args = append(args, "+login", "anonymous")
	case login.Username != "":
		pw, err := resolvePassword(login)
		if err != nil {
			return nil, err
		}
		args = append(args, "+login", login.Username, pw)
	default:
		return nil, fmt.Errorf("steamcmd: no login specified (set login_anonymous, login_username/login_password, " +
			"or login_password_file on the task; or configure default_login_anonymous/default_login_username on the " +
			"plugin's client config)")
	}

	updateArgs := []string{"+app_update", cfg.AppID}
	if cfg.Beta != "" {
		updateArgs = append(updateArgs, "-beta", cfg.Beta)
		if cfg.BetaPassword != "" {
			updateArgs = append(updateArgs, "-betapassword", cfg.BetaPassword)
		}
	}
	if cfg.Validate {
		updateArgs = append(updateArgs, "validate")
	}
	args = append(args, updateArgs...)
	args = append(args, "+quit")

	return args, nil
}

// resolvePassword returns the literal password to use, reading from
// password_file if set. password_file is expected to have been populated
// by a Nomad `template` block (Vault, Nomad variables, or literal --
// the driver does not care which) before StartTask runs.
func resolvePassword(login LoginConfig) (string, error) {
	if login.Password != "" {
		return login.Password, nil
	}
	if login.PasswordFile != "" {
		b, err := os.ReadFile(login.PasswordFile)
		if err != nil {
			return "", fmt.Errorf("steamcmd: reading password_file %q: %w", login.PasswordFile, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", fmt.Errorf("steamcmd: login.username set but neither password nor password_file provided")
}

// runInstall execs steamcmd with the given args, streams output to the
// provided writer (Nomad's task log files), and parses stdout for the
// real success/failure signal rather than trusting the exit code.
//
// onProgress, if non-nil, is called (throttled to progressThrottle) with a
// human-readable progress message parsed from steamcmd's own progress
// lines -- e.g. "downloading, 42.1%" -- so a caller can surface it as a
// Nomad TaskEvent the way the Docker driver surfaces image-pull layer
// progress while StartTask is still blocked.
func runInstall(ctx context.Context, steamCmdPath string, args []string, stdout, stderr io.Writer, onProgress func(string)) (*installResult, error) {
	cmd := exec.CommandContext(ctx, steamCmdPath, args...)

	pr, pw := io.Pipe()
	cmd.Stdout = io.MultiWriter(pw, stdout)
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	result := &installResult{}
	var lastProgress time.Time

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if m := reSuccess.FindStringSubmatch(line); m != nil {
				result.Success = true
				result.Message = line
			}
			if reAuthErr.MatchString(line) {
				result.AuthFailed = true
				result.Message = line
			}
			if m := reError.FindStringSubmatch(line); m != nil && !result.Success {
				result.Message = strings.TrimSpace(m[1])
			}
			if onProgress != nil {
				if m := reProgress.FindStringSubmatch(line); m != nil {
					if now := time.Now(); now.Sub(lastProgress) >= progressThrottle {
						lastProgress = now
						onProgress(fmt.Sprintf("%s, %s%%", strings.TrimSpace(m[1]), m[2]))
					}
				}
			}
		}
	}()

	runErr := cmd.Run()
	pw.Close()
	<-scanDone

	if cmd.ProcessState == nil {
		// The process never actually started (binary missing, exec
		// permission denied, etc) -- ProcessState is nil in this case and
		// calling ExitCode() on it would panic. Surface this as a clear
		// error instead of a nil pointer crash.
		return result, fmt.Errorf("steamcmd: failed to start process: %w", runErr)
	}
	result.ExitCode = cmd.ProcessState.ExitCode()

	// Success is checked first and is authoritative. A real auth failure
	// (bad credentials, no cached session) can never produce a genuine
	// "Success! App 'X' ..." line afterward -- so if we saw one, trust it,
	// even if an earlier line in the same run happened to match
	// reAuthErr. This matters because reAuthErr's patterns (especially
	// the bare "FAILED") are intentionally broad to catch real failures
	// reliably, which means they can also fire on benign transient lines
	// steamcmd prints during a normal login (retries, 2FA prompts on a
	// session that ultimately authenticates via a cached token, etc).
	// Confirmed in production: a real install completed successfully
	// end-to-end and was still reported as a Driver Failure with the
	// success line itself as the error message, because AuthFailed had
	// latched true on an earlier line and was never reconsidered once
	// Success came in after it.
	if result.Success {
		if result.AuthFailed {
			fmt.Fprintf(stderr, "steamcmd: an auth-related line was seen during this run, but a genuine success marker followed it; treating as success\n")
		}
		if runErr != nil {
			// Success marker present but process still returned an error --
			// trust the marker, but surface the anomaly in the log.
			fmt.Fprintf(stderr, "steamcmd: process returned error %v despite success marker; treating as success\n", runErr)
		}
		return result, nil
	}

	if result.AuthFailed {
		return result, fmt.Errorf("steamcmd: authentication failed: %s", result.Message)
	}

	// No success marker and no auth-failure marker: steamcmd exited
	// without a clear terminal signal either way -- treat as a failure
	// rather than trusting a bare exit code of 0 (the "steamcmd lied
	// about success" case).
	if result.Message == "" {
		result.Message = fmt.Sprintf("steamcmd exited %d with no success marker in output", result.ExitCode)
	}
	return result, fmt.Errorf("steamcmd: install/update did not complete: %s", result.Message)
}

// statfsFree returns free bytes on the filesystem containing path, used
// for a preflight disk-space check before kicking off a download.
func statfsFree(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
