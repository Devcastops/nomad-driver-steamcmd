package steamcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeSteamcmd writes a tiny shell script that mimics steamcmd's observed
// (mis)behaviors so we can test our parsing logic without the real binary
// or network access.
func fakeSteamcmd(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("fake steamcmd script requires a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "steamcmd")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755))
	return path
}

func TestRunInstall_GenuineSuccess(t *testing.T) {
	bin := fakeSteamcmd(t, `echo "Success! App '2394010' fully installed."
exit 0`)
	var stdout, stderr bytes.Buffer

	res, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, nil)
	require.NoError(t, err)
	require.True(t, res.Success)
}

func TestRunInstall_ExitZeroButNoSuccessMarker(t *testing.T) {
	// This is the well-known steamcmd footgun: exit 0 with no real
	// completion. Our parser must not trust the exit code alone.
	bin := fakeSteamcmd(t, `echo "doing something inconclusive"
exit 0`)
	var stdout, stderr bytes.Buffer

	res, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, nil)
	require.Error(t, err)
	require.False(t, res.Success)
}

func TestRunInstall_AuthFailure(t *testing.T) {
	bin := fakeSteamcmd(t, `echo "FAILED (Invalid Password)"
exit 5`)
	var stdout, stderr bytes.Buffer

	res, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, nil)
	require.Error(t, err)
	require.True(t, res.AuthFailed)
}

func TestRunInstall_SetlocaleWarningIsNotAnAuthFailure(t *testing.T) {
	// Regression test: steamcmd prints this harmless warning on every run
	// where the locale isn't generated. It contains "failed", which the old
	// case-insensitive bare-FAILED pattern treated as a login failure, so any
	// unrelated install error was reported as "authentication failed".
	bin := fakeSteamcmd(t, `echo "WARNING: setlocale('en_US.UTF-8') failed, using locale: 'C'. International characters may not work."
echo "Connecting anonymously to Steam Public...OK"
echo "Error! App '2278520' state is 0x6 after update job."
exit 8`)
	var stdout, stderr bytes.Buffer

	res, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, nil)
	require.Error(t, err)
	require.False(t, res.AuthFailed)
	require.Contains(t, err.Error(), "install/update did not complete")
	require.NotContains(t, err.Error(), "authentication failed")
	require.Contains(t, err.Error(), "state is 0x6 after update job")
}

func TestRunInstall_SetlocaleWarningAfterErrorKeepsRealMessage(t *testing.T) {
	// The setlocale line must not overwrite the real error message either,
	// whichever order the two lines arrive in.
	bin := fakeSteamcmd(t, `echo "Error! App '2278520' state is 0x6 after update job."
echo "WARNING: setlocale('en_US.UTF-8') failed, using locale: 'C'."
exit 8`)
	var stdout, stderr bytes.Buffer

	_, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "state is 0x6 after update job")
}

func TestReAuthErr_Patterns(t *testing.T) {
	for _, line := range []string{
		"Logging in user 'someone' to Steam Public...FAILED (Invalid Password)",
		"FAILED login with result code Invalid Password",
		"Logging in user 'someone' to Steam Public...FAILED (Rate Limit Exceeded)",
		"Login Failure: Account Logon Denied",
		"Two-factor code mismatch",
	} {
		require.True(t, reAuthErr.MatchString(line), "should match: %q", line)
	}
	for _, line := range []string{
		"WARNING: setlocale('en_US.UTF-8') failed, using locale: 'C'.",
		"Connecting anonymously to Steam Public...OK",
		"Error! App '2278520' state is 0x6 after update job.",
		"Success! App '2278520' fully installed.",
	} {
		require.False(t, reAuthErr.MatchString(line), "should not match: %q", line)
	}
}

func TestRunInstall_SuccessMarkerDespiteNonzeroExit(t *testing.T) {
	// Some steamcmd versions return nonzero even after a clean install.
	// A real success marker should still be trusted.
	bin := fakeSteamcmd(t, `echo "Success! App '2394010' fully updated."
exit 7`)
	var stdout, stderr bytes.Buffer

	res, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, nil)
	require.NoError(t, err)
	require.True(t, res.Success)
}

func TestRunInstall_AlreadyUpToDateCountsAsSuccess(t *testing.T) {
	bin := fakeSteamcmd(t, `echo "Success! App '2394010' already up to date."
exit 0`)
	var stdout, stderr bytes.Buffer

	res, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, nil)
	require.NoError(t, err)
	require.True(t, res.Success)
}

func TestRunInstall_SuccessAfterTransientAuthLikeLineIsNotAFailure(t *testing.T) {
	// Regression test for a real production bug: steamcmd sometimes
	// prints a line matching reAuthErr's (intentionally broad) patterns
	// during a normal login -- e.g. a retry, or 2FA-related text on a
	// session that ultimately authenticates via a cached token -- and
	// then goes on to install successfully. AuthFailed latching true on
	// that earlier line must not override a genuine success marker that
	// follows it. Confirmed in production: a real install completed
	// successfully end-to-end but was reported as a Driver Failure with
	// the success line itself as the (garbled) error message.
	bin := fakeSteamcmd(t, `echo "Login Failure retrying with cached session"
echo "Success! App '2915550' fully installed."
exit 0`)
	var stdout, stderr bytes.Buffer

	res, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, nil)
	require.NoError(t, err)
	require.True(t, res.Success)
	require.True(t, res.AuthFailed, "the transient line should still be recorded, just not treated as fatal")
}

func TestRunInstall_ProgressCallback(t *testing.T) {
	bin := fakeSteamcmd(t, `echo "Update state (0x61) downloading, progress: 12.34 (100 / 811)"
echo "Success! App '90' fully installed."
exit 0`)
	var stdout, stderr bytes.Buffer
	var messages []string

	_, err := runInstall(context.Background(), bin, []string{"+quit"}, &stdout, &stderr, func(msg string) {
		messages = append(messages, msg)
	})
	require.NoError(t, err)
	require.NotEmpty(t, messages)
	require.Contains(t, messages[0], "12.34")
}
