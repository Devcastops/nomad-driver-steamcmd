package steamcmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeConfigFiles(t *testing.T) {
	got := mergeConfigFiles([]map[string]string{{"a": "1", "b": "2"}, {"b": "3"}, nil})
	if len(got) != 2 || got["a"] != "1" || got["b"] != "3" {
		t.Errorf("got %v", got)
	}
	if got := mergeConfigFiles(nil); len(got) != 0 {
		t.Errorf("nil input: %v", got)
	}
}

func TestStageConfigFiles(t *testing.T) {
	taskDir := t.TempDir()
	installDir := filepath.Join(taskDir, "install")
	if err := os.MkdirAll(filepath.Join(taskDir, "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "local", "a.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Creates parent dirs, copies relative source.
	if err := stageConfigFiles(taskDir, installDir, map[string]string{"local/a.json": "cfg/sub/a.json"}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(installDir, "cfg", "sub", "a.json")
	if b, err := os.ReadFile(dst); err != nil || string(b) != "new" {
		t.Fatalf("dst = %q, %v", b, err)
	}

	// Overwrites an existing destination.
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stageConfigFiles(taskDir, installDir, map[string]string{"local/a.json": "cfg/sub/a.json"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Errorf("not overwritten: %q", b)
	}

	// Absolute source works.
	abs := filepath.Join(taskDir, "local", "a.json")
	if err := stageConfigFiles(taskDir, installDir, map[string]string{abs: "b.json"}); err != nil {
		t.Fatal(err)
	}

	// Missing source, escaping and absolute destinations are errors.
	if err := stageConfigFiles(taskDir, installDir, map[string]string{"local/missing": "x"}); err == nil {
		t.Error("missing source should error")
	}
	for _, bad := range []string{"../x", "a/../../x", "/etc/x", "", "."} {
		if err := stageConfigFiles(taskDir, installDir, map[string]string{"local/a.json": bad}); err == nil {
			t.Errorf("destination %q should be rejected", bad)
		}
	}

	// No leftover temp files.
	entries, _ := os.ReadDir(installDir)
	for _, e := range entries {
		if len(e.Name()) > 6 && e.Name()[:7] == ".stage-" {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}
