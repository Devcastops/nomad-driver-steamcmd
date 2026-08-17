package steamcmd

import (
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
)

// PluginConfig is the client-level (agent-wide) configuration for the
// plugin, set via a `plugin "steamcmd" { config { ... } }` stanza in the
// Nomad client's agent config. It provides fleet-wide defaults that a task
// can omit and inherit from.
type PluginConfig struct {
	SteamCmdPath  string      `codec:"steamcmd_path"`
	DefaultLogin  LoginConfig `codec:"default_login"`
	InstallRoot   string      `codec:"install_root"`
	MaxConcurrent int         `codec:"max_concurrent_installs"`
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
	"default_login": hclspec.NewBlock("default_login", false, loginSpec),
})

// TaskConfig is the per-task configuration set in a job spec's
// `driver = "steamcmd"` task's `config { ... }` block.
type TaskConfig struct {
	AppID          string        `codec:"app_id"`
	InstallDir     string        `codec:"install_dir"`
	Beta           string        `codec:"beta"`
	BetaPassword   string        `codec:"beta_password"`
	Validate       bool          `codec:"validate"`
	Login          *LoginConfig  `codec:"login"`
	Launch         *LaunchConfig `codec:"launch"`
	UpdateOnStart  bool          `codec:"update_on_start"`
	InstallTimeout string        `codec:"install_timeout"`
}

// LoginConfig describes how steamcmd should authenticate. A task may omit
// this entirely to inherit PluginConfig.DefaultLogin.
type LoginConfig struct {
	Anonymous    bool   `codec:"anonymous"`
	Username     string `codec:"username"`
	Password     string `codec:"password"`
	PasswordFile string `codec:"password_file"`
}

// LaunchConfig describes the process to exec once install/update completes.
// If nil, the task is install-only and exits after steamcmd finishes.
type LaunchConfig struct {
	Command string            `codec:"command"`
	Args    []string          `codec:"args"`
	Env     map[string]string `codec:"env"`
}

var loginSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	"anonymous":     hclspec.NewAttr("anonymous", "bool", false),
	"username":      hclspec.NewAttr("username", "string", false),
	"password":      hclspec.NewAttr("password", "string", false),
	"password_file": hclspec.NewAttr("password_file", "string", false),
})

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
	"beta":            hclspec.NewAttr("beta", "string", false),
	"beta_password":   hclspec.NewAttr("beta_password", "string", false),
	"validate":        hclspec.NewAttr("validate", "bool", false),
	"update_on_start": hclspec.NewAttr("update_on_start", "bool", false),
	"install_timeout": hclspec.NewAttr("install_timeout", "string", false),
	"login":           hclspec.NewBlock("login", false, loginSpec),
	"launch":          hclspec.NewBlock("launch", false, launchSpec),
})

// resolveLogin returns the effective login to use for a task: the task's
// own `login` block if present (full override, no field-level merge),
// otherwise the plugin-level default.
//
// Deliberately does not treat "task != nil" alone as "the operator
// specified a login" -- in practice an omitted optional `login` block has
// been observed decoding to a non-nil pointer to a zero-value LoginConfig
// rather than a true nil (this surfaced as install-only tasks correctly
// falling back to the plugin's default_login while an otherwise-identical
// task with a `launch` block did not, using the exact same driver config).
// Checking for a meaningfully empty struct instead is robust regardless of
// the exact decoding cause, and is arguably more sensible behavior anyway:
// a `login {}` block with nothing set should fall back to the fleet
// default, not be treated as "no login at all".
func resolveLogin(task *LoginConfig, def LoginConfig) LoginConfig {
	if task != nil && !isZeroLogin(*task) {
		return *task
	}
	return def
}

func isZeroLogin(l LoginConfig) bool {
	return !l.Anonymous && l.Username == "" && l.Password == "" && l.PasswordFile == ""
}
