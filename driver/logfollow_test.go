package steamcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, b *syncBuf, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; got %q", want, b.String())
}

func startFollow(t *testing.T, path string) (*syncBuf, func()) {
	t.Helper()
	out := &syncBuf{}
	stale := snapshotFiles([]string{path}) // taken synchronously, before any writer starts
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		followLogs(ctx, []string{path}, stale, out, 10*time.Millisecond)
	}()
	return out, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("followLogs did not return after cancel")
		}
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestResolveFollowPaths(t *testing.T) {
	got := resolveFollowPaths("/i", []string{"logs/a.log", "/abs/b.log", ""})
	want := []string{filepath.Join("/i", "logs/a.log"), "/abs/b.log"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestFollow_FileCreatedAfterStart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs", "s.log")
	out, stop := startFollow(t, p)
	defer stop()
	time.Sleep(50 * time.Millisecond)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, "one\ntwo\n")
	waitFor(t, out, "one\ntwo\n")
	appendFile(t, p, "thr")
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(out.String(), "thr") {
		t.Error("partial line was emitted before its newline")
	}
	appendFile(t, p, "ee\n")
	waitFor(t, out, "three\n")
}

func TestFollow_IgnoresStaleFileUntilReplaced(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.log")
	appendFile(t, p, "old run line\n")
	time.Sleep(20 * time.Millisecond)
	out, stop := startFollow(t, p)
	defer stop()
	time.Sleep(100 * time.Millisecond)
	if out.String() != "" {
		t.Fatalf("stale content leaked: %q", out.String())
	}
	// Game rotates the old log away and writes a fresh one.
	if err := os.Rename(p, filepath.Join(dir, "s.log.1")); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, "new run line\n")
	waitFor(t, out, "new run line\n")
	if strings.Contains(out.String(), "old run") {
		t.Errorf("stale content leaked: %q", out.String())
	}
}

func TestFollow_StaleFileModifiedAfterStartIsFollowed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.log")
	appendFile(t, p, "old line one\nold line two\n")
	time.Sleep(20 * time.Millisecond)
	out, stop := startFollow(t, p)
	defer stop()
	time.Sleep(30 * time.Millisecond)
	// Truncated in place and rewritten (smaller) by the new run.
	if err := os.WriteFile(p, []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, out, "fresh\n")
	if strings.Contains(out.String(), "old line") {
		t.Errorf("stale content leaked: %q", out.String())
	}
}

func TestFollow_AppendStyleLogSkipsPreviousContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.log")
	appendFile(t, p, "previous run line\n")
	time.Sleep(20 * time.Millisecond)
	out, stop := startFollow(t, p)
	defer stop()
	time.Sleep(30 * time.Millisecond)
	appendFile(t, p, "this run line\n")
	waitFor(t, out, "this run line\n")
	if strings.Contains(out.String(), "previous run") {
		t.Errorf("previous content replayed: %q", out.String())
	}
}

func TestFollow_FileCreatedImmediatelyAfterStartIsNotMistakenForStale(t *testing.T) {
	// Regression: the "does it already exist?" check used to run inside the
	// goroutine, so a server creating its log right away looked stale.
	p := filepath.Join(t.TempDir(), "s.log")
	out, stop := startFollow(t, p)
	defer stop()
	appendFile(t, p, "instant\n") // no sleep
	waitFor(t, out, "instant\n")
}

func TestFollow_RotationAndTruncation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.log")
	out, stop := startFollow(t, p)
	defer stop()
	appendFile(t, p, "a\n")
	waitFor(t, out, "a\n")
	// Rotate.
	if err := os.Rename(p, filepath.Join(dir, "s.log.1")); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, "bbbbbbbbbbbb\n")
	waitFor(t, out, "bbbbbbbbbbbb\n")
	// Truncate in place and rewrite with less content than before.
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(p, []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, out, "c\n")
}

func TestFollow_FinalDrainAndPartialFlushOnCancel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.log")
	out := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		followLogs(ctx, []string{p}, nil, out, time.Hour) // poll never fires; only the final step runs
	}()
	time.Sleep(30 * time.Millisecond)
	appendFile(t, p, "last words\nno newline")
	cancel()
	<-done
	if got := out.String(); got != "last words\nno newline\n" {
		t.Errorf("got %q", got)
	}
}
