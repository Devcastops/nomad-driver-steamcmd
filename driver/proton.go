package steamcmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	runnerWine   = "wine"
	runnerProton = "proton"
)

// windowsRuntime describes how a platform="windows" launch command gets
// executed on the Linux client: bare wine (the original behaviour, and the
// zero value) or a Proton build.
//
// Proton is Valve's pinned Wine fork. It is invoked as
// `<proton_path>/proton run <exe> <args>` and ignores WINEPREFIX; it wants
// STEAM_COMPAT_DATA_PATH (it creates `pfx/` underneath) and
// STEAM_COMPAT_CLIENT_INSTALL_PATH instead.
type windowsRuntime struct {
	Runner         string // "wine" (or "" == wine) | "proton"
	ProtonPath     string // proton dir (containing the `proton` script) or the script itself
	CompatDataPath string // absolute; STEAM_COMPAT_DATA_PATH
	ClientPath     string // absolute; STEAM_COMPAT_CLIENT_INSTALL_PATH
	WinePrefix     string // wine runner only
}

func (w windowsRuntime) isProton() bool { return w.Runner == runnerProton }

// resolveRunner applies task -> plugin -> "wine" precedence and validates.
func resolveRunner(task, plugin string) (string, error) {
	r := task
	if r == "" {
		r = plugin
	}
	switch r {
	case "", runnerWine:
		return runnerWine, nil
	case runnerProton:
		return runnerProton, nil
	default:
		return "", fmt.Errorf("invalid runner %q (must be \"wine\" or \"proton\")", r)
	}
}

// protonScript returns the path to the `proton` script given either the
// Proton directory or the script path itself.
func protonScript(p string) string {
	if filepath.Base(p) == "proton" {
		return p
	}
	return filepath.Join(p, "proton")
}

// protonWineserver locates the wineserver binary shipped inside a Proton
// build. Valve's builds use files/bin, GE-Proton uses files/bin as well,
// older/community layouts use dist/bin. Returns "" if none is found.
func protonWineserver(p string) string {
	dir := p
	if filepath.Base(p) == "proton" {
		dir = filepath.Dir(p)
	}
	for _, rel := range []string{"files/bin/wineserver", "dist/bin/wineserver"} {
		c := filepath.Join(dir, rel)
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// resolveWindowsRuntime builds the effective runtime for a task. taskRoot is
// the task directory that relative paths resolve against. home is used to
// find an existing Steam install for STEAM_COMPAT_CLIENT_INSTALL_PATH.
func resolveWindowsRuntime(task TaskConfig, plugin PluginConfig, taskRoot, home string) (windowsRuntime, error) {
	runner, err := resolveRunner(task.Runner, plugin.DefaultRunner)
	if err != nil {
		return windowsRuntime{}, err
	}
	if runner == runnerWine {
		return windowsRuntime{Runner: runnerWine, WinePrefix: resolveWinePrefix(task.WinePrefix, plugin.DefaultWinePrefix)}, nil
	}

	protonPath := task.ProtonPath
	if protonPath == "" {
		protonPath = plugin.DefaultProtonPath
	}
	if protonPath == "" {
		return windowsRuntime{}, fmt.Errorf("runner = \"proton\" requires proton_path (task) or default_proton_path (plugin)")
	}
	if !filepath.IsAbs(protonPath) {
		return windowsRuntime{}, fmt.Errorf("proton_path %q must be absolute", protonPath)
	}

	compat := task.CompatDataPath
	if compat == "" {
		compat = "local/compat"
	}
	if !filepath.IsAbs(compat) {
		compat = filepath.Join(taskRoot, compat)
	}

	client := task.CompatClientPath
	if client == "" {
		client = defaultSteamClientPath(home, compat)
	}
	return windowsRuntime{Runner: runnerProton, ProtonPath: protonPath, CompatDataPath: compat, ClientPath: client}, nil
}

// defaultSteamClientPath prefers an existing Steam install (~/.steam/steam,
// as left by steamcmd's own first run) and falls back to the compat data
// dir, which Proton accepts for headless use.
func defaultSteamClientPath(home, fallback string) string {
	if home != "" {
		c := filepath.Join(home, ".steam", "steam")
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return fallback
}

// validate checks the runtime's host-side prerequisites and prepares dirs.
func (w windowsRuntime) prepare() error {
	if !w.isProton() {
		return nil
	}
	script := protonScript(w.ProtonPath)
	st, err := os.Stat(script)
	if err != nil || st.IsDir() {
		return fmt.Errorf("proton script not found at %q (check proton_path)", script)
	}
	if err := os.MkdirAll(w.CompatDataPath, 0o755); err != nil {
		return fmt.Errorf("failed to create compat_data_path %q: %w", w.CompatDataPath, err)
	}
	return nil
}

// env returns the environment variables this runtime needs.
func (w windowsRuntime) env(appID, installDir string) []string {
	if w.isProton() {
		e := []string{
			"STEAM_COMPAT_DATA_PATH=" + w.CompatDataPath,
			"STEAM_COMPAT_CLIENT_INSTALL_PATH=" + w.ClientPath,
			"STEAM_COMPAT_INSTALL_PATH=" + installDir,
		}
		if appID != "" {
			e = append(e, "SteamAppId="+appID, "SteamGameId="+appID)
		}
		return e
	}
	if w.WinePrefix != "" {
		return []string{"WINEPREFIX=" + w.WinePrefix}
	}
	return nil
}

// executable is the program that runs the .exe, plus any fixed leading args.
func (w windowsRuntime) executable() (string, []string) {
	if w.isProton() {
		return protonScript(w.ProtonPath), []string{"run"}
	}
	return "wine", nil
}

// wineCleanup is what's needed to kill a prefix's leftover wineserver after
// the task's process group is gone. It's persisted in taskState so it
// survives driver/client restarts.
type wineCleanup struct {
	Bin string
	Env []string
}

// cleanup returns nil when there is nothing to clean up.
func (w windowsRuntime) cleanup() *wineCleanup {
	if w.isProton() {
		bin := protonWineserver(w.ProtonPath)
		if bin == "" {
			return nil
		}
		return &wineCleanup{Bin: bin, Env: []string{"WINEPREFIX=" + filepath.Join(w.CompatDataPath, "pfx")}}
	}
	c := &wineCleanup{Bin: "wineserver"}
	if w.WinePrefix != "" {
		c.Env = []string{"WINEPREFIX=" + w.WinePrefix}
	}
	return c
}

// run executes `wineserver -k`. wineserver daemonizes, so it routinely
// escapes the task's process group and would otherwise linger (holding the
// prefix) after every stop.
func (c *wineCleanup) run(timeout time.Duration) error {
	if c == nil || c.Bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, "-k")
	cmd.Env = append(os.Environ(), c.Env...)
	return cmd.Run()
}
