package steamcmd

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildArgs_Anonymous(t *testing.T) {
	cfg := TaskConfig{AppID: "2394010"}
	login := LoginConfig{Anonymous: true}

	args, err := buildArgs(cfg, login, "/tmp/install")
	require.NoError(t, err)
	require.Equal(t, []string{
		"+force_install_dir", "/tmp/install",
		"+login", "anonymous",
		"+app_update", "2394010",
		"+quit",
	}, args)
}

func TestBuildArgs_ValidateAndBeta(t *testing.T) {
	cfg := TaskConfig{AppID: "2394010", Beta: "experimental", BetaPassword: "secret", Validate: true}
	login := LoginConfig{Anonymous: true}

	args, err := buildArgs(cfg, login, "/tmp/install")
	require.NoError(t, err)
	require.Contains(t, args, "-beta")
	require.Contains(t, args, "experimental")
	require.Contains(t, args, "-betapassword")
	require.Contains(t, args, "validate")
}

func TestBuildArgs_PlatformOverride(t *testing.T) {
	cfg := TaskConfig{AppID: "2915550", Platform: "windows"}
	login := LoginConfig{Anonymous: true}

	args, err := buildArgs(cfg, login, "/tmp/install")
	require.NoError(t, err)
	require.Equal(t, []string{
		"+@sSteamCmdForcePlatformType", "windows",
		"+force_install_dir", "/tmp/install",
		"+login", "anonymous",
		"+app_update", "2915550",
		"+quit",
	}, args)
}

func TestBuildArgs_InvalidPlatformErrors(t *testing.T) {
	cfg := TaskConfig{AppID: "2915550", Platform: "amiga"}
	login := LoginConfig{Anonymous: true}

	_, err := buildArgs(cfg, login, "/tmp/install")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid platform")
}

func TestBuildArgs_NoLoginErrors(t *testing.T) {
	cfg := TaskConfig{AppID: "2394010"}
	login := LoginConfig{}

	_, err := buildArgs(cfg, login, "/tmp/install")
	require.Error(t, err)
}

func TestBuildArgs_PasswordFile(t *testing.T) {
	f := t.TempDir() + "/pw"
	require.NoError(t, os.WriteFile(f, []byte("hunter2\n"), 0o600))

	cfg := TaskConfig{AppID: "1"}
	login := LoginConfig{Username: "someuser", PasswordFile: f}

	args, err := buildArgs(cfg, login, "/tmp/install")
	require.NoError(t, err)
	require.Contains(t, args, "hunter2")
}

func TestResolveLogin_TaskOverridesPluginDefault(t *testing.T) {
	def := LoginConfig{Anonymous: true}
	task := LoginConfig{Username: "explicit", Password: "pw"}

	got := resolveLogin(task, def)
	require.Equal(t, "explicit", got.Username)
	require.False(t, got.Anonymous)
}

func TestResolveLogin_FallsBackToPluginDefault(t *testing.T) {
	def := LoginConfig{Anonymous: true}
	task := LoginConfig{}

	got := resolveLogin(task, def)
	require.True(t, got.Anonymous)
}

func TestTaskConfig_LoginBuildsFromFlatFields(t *testing.T) {
	cfg := TaskConfig{LoginAnonymous: true}
	require.True(t, cfg.Login().Anonymous)

	cfg2 := TaskConfig{LoginUsername: "explicit", LoginPassword: "pw"}
	got := cfg2.Login()
	require.Equal(t, "explicit", got.Username)
	require.False(t, got.Anonymous)
}

func TestPluginConfig_DefaultLoginBuildsFromFlatFields(t *testing.T) {
	cfg := PluginConfig{DefaultLoginAnonymous: true}
	require.True(t, cfg.DefaultLogin().Anonymous)

	cfgFalse := PluginConfig{DefaultLoginAnonymous: false}
	require.False(t, cfgFalse.DefaultLogin().Anonymous)

	cfgEmpty := PluginConfig{}
	require.False(t, cfgEmpty.DefaultLogin().Anonymous)
}
