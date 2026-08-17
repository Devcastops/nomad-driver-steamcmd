package steamcmd

import (
	"strconv"

	"github.com/hashicorp/nomad/plugins/shared/hclspec"
)

// PluginConfig is the client-level (agent-wide) configuration for the
// plugin, set via a `plugin "steamcmd" { config { ... } }` stanza in the
// Nomad client's agent config. It provides fleet-wide defaults that a task
// can omit and inherit from.
//
// DefaultLoginAnonymous is deliberately a STRING ("true"/"false"), not a
// native bool. Two separate fixes were tried and both failed identically
// (a nested default_login{} block, then a flat bool attribute wrapped in
// hclspec.NewDefault) -- in every case a string attribute at this same
// agent-config layer (steamcmd_path) decoded correctly while the bool
// attribute came back false regardless of what the HCL actually set.
// That's a consistent enough pattern (string: always works, bool: always
// fails, at this specific layer) to act on directly rather than chase the
// exact internal cause further. Parsed via strconv.ParseBool in
// DefaultLogin() below.
type PluginConfig struct {
	SteamCmdPath             string `codec:"steamcmd_path"`
	DefaultLoginAnonymousStr string `codec:"default_login_anonymous"`
	DefaultLoginUsername     string `codec:"default_login_username"`
	DefaultLoginPassword     string `codec:"default_login_password"`
	DefaultLoginPasswordFile string `codec:"default_login_password_file"`
	InstallRoot              string `codec:"install_root"`
	MaxConcurrent            int    `codec:"max_concurrent_installs"`
}

// DefaultLogin builds a LoginConfig from the flat default_login_* fields.
func (p PluginConfig) DefaultLogin() LoginConfig {
	anon, _ := strconv.ParseBool(p.DefaultLoginAnonymousStr) // "" -> false, no error handling needed
	return LoginConfig{
		Anonymous:    anon,
		Username:     p.DefaultLoginUsername,
		Password:     p.DefaultLoginPassword,
		PasswordFile: p.DefaultLoginPasswordFile,
	}
}

// configSpec is the hclspec for the plugin-level config block. Every
// optional attribute is wrapped in hclspec.NewDefault. default_login_anonymous
// is typed "string" rather than "bool" -- see the PluginConfig doc comment.
var configSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	"steamcmd_path": hclspec.NewDefault(
		hclspec.NewAttr("steamcmd_path", "string", false),
		hclspec.NewLiteral(`"steamcmd"`),
	),
	"install_root": hclspec.NewDefault(
		hclspec.NewAttr("install_root", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"max_concurrent_installs": hclspec.NewDefault(
		hclspec.NewAttr("max_concurrent_installs", "number", false),
		hclspec.NewLiteral("0"),
	),
	"default_login_anonymous": hclspec.NewDefault(
		hclspec.NewAttr("default_login_anonymous", "string", false),
		hclspec.NewLiteral(`"false"`),
	),
	"default_login_username": hclspec.NewDefault(
		hclspec.NewAttr("default_login_username", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"default_login_password": hclspec.NewDefault(
		hclspec.NewAttr("default_login_password", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"default_login_password_file": hclspec.NewDefault(
		hclspec.NewAttr("default_login_password_file", "string", false),
		hclspec.NewLiteral(`""`),
	),
})

// TaskConfig is the per-task configuration set in a job spec's
// `driver = "steamcmd"` task's `config { ... }` block.
//
// Login fields are flat here too (login_anonymous, not a nested login{}
// block), for the same reason as PluginConfig above.
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
type LaunchConfig struct {
	Command string            `codec:"command"`
	Args    []string          `codec:"args"`
	Env     map[string]string `codec:"env"`
}

var launchSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	"command": hclspec.NewAttr("command", "string", true),
	"args": hclspec.NewDefault(
		hclspec.NewAttr("args", "list(string)", false),
		hclspec.NewLiteral("[]"),
	),
	"env": hclspec.NewDefault(
		hclspec.NewAttr("env", "map(string)", false),
		hclspec.NewLiteral("{}"),
	),
})

// taskConfigSpec is the hclspec Nomad uses to validate/decode a task's
// `config` block for this driver. Every optional attribute is wrapped in
// hclspec.NewDefault -- see the PluginConfig doc comment for why.
var taskConfigSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	"app_id": hclspec.NewAttr("app_id", "string", true),
	"install_dir": hclspec.NewDefault(
		hclspec.NewAttr("install_dir", "string", false),
		hclspec.NewLiteral(`"local/steamapp"`),
	),
	"beta": hclspec.NewDefault(
		hclspec.NewAttr("beta", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"beta_password": hclspec.NewDefault(
		hclspec.NewAttr("beta_password", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"validate": hclspec.NewDefault(
		hclspec.NewAttr("validate", "bool", false),
		hclspec.NewLiteral("false"),
	),
	"update_on_start": hclspec.NewDefault(
		hclspec.NewAttr("update_on_start", "bool", false),
		hclspec.NewLiteral("false"),
	),
	"install_timeout": hclspec.NewDefault(
		hclspec.NewAttr("install_timeout", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"login_anonymous": hclspec.NewDefault(
		hclspec.NewAttr("login_anonymous", "bool", false),
		hclspec.NewLiteral("false"),
	),
	"login_username": hclspec.NewDefault(
		hclspec.NewAttr("login_username", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"login_password": hclspec.NewDefault(
		hclspec.NewAttr("login_password", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"login_password_file": hclspec.NewDefault(
		hclspec.NewAttr("login_password_file", "string", false),
		hclspec.NewLiteral(`""`),
	),
	"launch": hclspec.NewBlock("launch", false, launchSpec),
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
