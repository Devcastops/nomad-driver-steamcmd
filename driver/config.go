package steamcmd

import (
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
)

// PluginConfig is the client-level (agent-wide) configuration for the
// plugin, set via a `plugin "steamcmd" { config { ... } }` stanza in the
// Nomad client's agent config. It provides fleet-wide defaults that a task
// can omit and inherit from.
//
// Login fields are intentionally FLAT (default_login_anonymous, not a
// nested default_login{} block). A nested hclspec.NewBlock here was
// observed to decode incorrectly at the agent-config layer specifically
// (top-level flat attributes like steamcmd_path decoded fine; the nested
// block's contents were silently lost, leaving every field at its zero
// value even when the HCL clearly set them). Flattening sidesteps that
// rather than depending on a mechanism that's proven broken in this path.
type PluginConfig struct {
	SteamCmdPath             string `codec:"steamcmd_path"`
	DefaultLoginAnonymous    bool   `codec:"default_login_anonymous"`
	DefaultLoginUsername     string `codec:"default_login_username"`
	DefaultLoginPassword     string `codec:"default_login_password"`
	DefaultLoginPasswordFile string `codec:"default_login_password_file"`
	InstallRoot              string `codec:"install_root"`
	MaxConcurrent            int    `codec:"max_concurrent_installs"`
}

// DefaultLogin builds a LoginConfig from the flat default_login_* fields.
func (p PluginConfig) DefaultLogin() LoginConfig {
	return LoginConfig{
		Anonymous:    p.DefaultLoginAnonymous,
		Username:     p.DefaultLoginUsername,
		Password:     p.DefaultLoginPassword,
		PasswordFile: p.DefaultLoginPasswordFile,
	}
}

// configSpec is the hclspec for the plugin-level config block.
var configSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	"steamcmd_path": hclspec.NewDefault(
		hclspec.NewAttr("steamcmd_path", "string", false),
		hclspec.NewLiteral(`"steamcmd"`),
	),
	"install_root": hclspec.NewAttr("install_root", "string", false),
	"max_concurrent_installs": hclspec.NewDefault(
		hclspec.NewAttr("max_concurrent_installs", "number", false),
		hclspec.NewLiteral("0"),
	),
	"default_login_anonymous":     hclspec.NewAttr("default_login_anonymous", "bool", false),
	"default_login_username":      hclspec.NewAttr("default_login_username", "string", false),
	"default_login_password":      hclspec.NewAttr("default_login_password", "string", false),
	"default_login_password_file": hclspec.NewAttr("default_login_password_file", "string", false),
})

// TaskConfig is the per-task configuration set in a job spec's
// `driver = "steamcmd"` task's `config { ... }` block.
//
// Login fields are flat here too (login_anonymous, not a nested login{}
// block), for the same reason as PluginConfig above -- and because a task
// config with a `launch` block sibling was the exact case where the
// nested `login` block was observed decoding incorrectly, so both layers
// get the same fix rather than leaving one on an unproven mechanism.
type TaskConfig struct {
	AppID             string        `codec:"app_id"`
	InstallDir        string        `codec:"install_dir"`
	Beta              string        `codec:"beta"`
	BetaPassword      string        `codec:"beta_password"`
	Validate          bool          `codec:"validate"`
	LoginAnonymous    bool          `codec:"login_anonymous"`
	LoginUsername     string        `codec:"login_username"`
	LoginPassword     string        `codec:"login_password"`
	LoginPasswordFile string        `codec:"login_password_file"`
	Launch            *LaunchConfig `codec:"launch"`
	UpdateOnStart     bool          `codec:"update_on_start"`
	InstallTimeout    string        `codec:"install_timeout"`
}

// Login builds a LoginConfig from the task's flat login_* fields.
func (t TaskConfig) Login() LoginConfig {
	return LoginConfig{
		Anonymous:    t.LoginAnonymous,
		Username:     t.LoginUsername,
		Password:     t.LoginPassword,
		PasswordFile: t.LoginPasswordFile,
	}
}

// LoginConfig describes how steamcmd should authenticate. Built from
// either TaskConfig's or PluginConfig's flat login_*/default_login_*
// fields -- it is a plain internal convenience type, not itself an
// hclspec decode target.
type LoginConfig struct {
	Anonymous    bool
	Username     string
	Password     string
	PasswordFile string
}

// LaunchConfig describes the process to exec once install/update completes.
// If nil, the task is install-only and exits after steamcmd finishes.
//
// Left as a nested hclspec block for now, unlike login above: its presence
// (launch_is_nil) has been observed decoding correctly, and its contents
// haven't yet been exercised far enough by real runs to know whether they
// have the same problem. If a run gets past login and Launch.Command
// turns out empty/wrong, flatten this the same way.
type LaunchConfig struct {
	Command string            `codec:"command"`
	Args    []string          `codec:"args"`
	Env     map[string]string `codec:"env"`
}

var launchSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	"command": hclspec.NewAttr("command", "string", true),
	"args":    hclspec.NewAttr("args", "list(string)", false),
	"env":     hclspec.NewAttr("env", "map(string)", false),
})

// taskConfigSpec is the hclspec Nomad uses to validate/decode a task's
// `config` block for this driver.
var taskConfigSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	"app_id": hclspec.NewAttr("app_id", "string", true),
	"install_dir": hclspec.NewDefault(
		hclspec.NewAttr("install_dir", "string", false),
		hclspec.NewLiteral(`"local/steamapp"`),
	),
	"beta":                hclspec.NewAttr("beta", "string", false),
	"beta_password":       hclspec.NewAttr("beta_password", "string", false),
	"validate":            hclspec.NewAttr("validate", "bool", false),
	"update_on_start":     hclspec.NewAttr("update_on_start", "bool", false),
	"install_timeout":     hclspec.NewAttr("install_timeout", "string", false),
	"login_anonymous":     hclspec.NewAttr("login_anonymous", "bool", false),
	"login_username":      hclspec.NewAttr("login_username", "string", false),
	"login_password":      hclspec.NewAttr("login_password", "string", false),
	"login_password_file": hclspec.NewAttr("login_password_file", "string", false),
	"launch":              hclspec.NewBlock("launch", false, launchSpec),
})

// resolveLogin returns the effective login to use for a task: the task's
// own login_* fields if any are meaningfully set, otherwise the plugin's
// default_login_* fields.
func resolveLogin(task LoginConfig, def LoginConfig) LoginConfig {
	if !isZeroLogin(task) {
		return task
	}
	return def
}

func isZeroLogin(l LoginConfig) bool {
	return !l.Anonymous && l.Username == "" && l.Password == "" && l.PasswordFile == ""
}
