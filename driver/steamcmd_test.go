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
