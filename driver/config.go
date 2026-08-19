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
// SUSPECTED VERSION-SPECIFIC NOMAD BUG, NOT AN ISSUE WITH THIS SCHEMA:
// explicit values set in the agent's own `nomad.hcl` for this block have
// been observed NOT reaching SetConfig on Nomad v1.9.3 -- the field
// silently falls back to whatever this package's own hclspec.NewDefault
// literal says, regardless of what the HCL actually sets. Proven directly
// via a diagnostic log (default_login_anonymous decoded as a genuine
// 5-character string "false" -- exactly this schema's own default --
// even though the agent config clearly set it to "true"), and reproduced
// on two independent machines (CI and a real client), fully isolated in
// its own config file with no other plugin blocks nearby.
//
// This is confirmed NOT to be a bug in this package's hclspec usage: the
// exact same SetConfig/ConfigSchema/PluginInfo wiring was compared
// line-by-line against hashicorp/nomad-driver-podman's current source (a
// real, actively-maintained external plugin using the identical
// `base.MsgPackDecode` mechanism), including a bare unwrapped-string
// attribute (socket_path) documented as working for podman -- our schema
// matches that pattern exactly. Since podman's tested Nomad version isn't
// known and this module was previously pinned to v1.9.3, the working
// theory is a version-specific Nomad regression rather than anything
// here. The module (and the Nomad binary version `.github/workflows/e2e.yml`
// tests against) is now pinned to v1.10.0 specifically to test that
// theory -- check the "steamcmd plugin config loaded" diagnostic log line
// in SetConfig on that version before assuming this is fixed. If it's
// still falling back to defaults on v1.10.0, the theory is wrong and the
// next thing to check is a dependency-version mismatch in whichever
// msgpack library base.MsgPackDecode resolves to, since this repo has
// never had a committed, pinned go.sum.
//
// Confirmed reliable regardless: TaskConfig's login_* fields (job-spec
// parsing, a different Nomad code path) -- proven with real credentials
// against real steamcmd auth rejection, not just a decode check. If you
// need the plugin-level default to be a *specific* non-anonymous account
// and can't confirm this block works on your version, set
// login_username/login_password explicitly on every task instead.
//
// DefaultLoginAnonymous is a STRING ("true"/"false"), not a native bool --
// parsed via strconv.ParseBool in DefaultLogin() below. That part of the
// investigation (nested block, then native bool, both failed identically)
// turned out to be a red herring once the real cause was found, but the
// string type is kept since it works and there's no reason to revert it.
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
		hclspec.NewLiteral(`"true"`),
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
