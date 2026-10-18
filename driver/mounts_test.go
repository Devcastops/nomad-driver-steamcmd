package steamcmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/nomad/plugins/drivers"
)

func TestMountTarget(t *testing.T) {
	root := "/alloc/task"
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"data", "/alloc/task/data", false},
		{"/data", "/alloc/task/data", false},
		{"/a/b", "/alloc/task/a/b", false},
		{"../evil", "", true},
		{"/../evil", "", true}, // leading slash is stripped, leaving ../evil
		{"a/../../evil", "", true},
		{"/", "", true},
	}
	for _, tc := range cases {
		got, err := mountTarget(root, tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("mountTarget(%q) = %q, %v; want %q (err=%v)", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestSetupMounts(t *testing.T) {
	taskDir := t.TempDir()
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	warns, err := setupMounts(taskDir, []*drivers.MountConfig{
		{HostPath: host, TaskPath: "/data", Readonly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 {
		t.Errorf("want 1 read-only warning, got %v", warns)
	}
	if b, err := os.ReadFile(filepath.Join(taskDir, "data", "f")); err != nil || string(b) != "x" {
		t.Fatalf("file not visible through mount: %v %q", err, b)
	}

	// Idempotent.
	if _, err := setupMounts(taskDir, []*drivers.MountConfig{{HostPath: host, TaskPath: "data"}}); err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Stale symlink to another host path is replaced.
	other := t.TempDir()
	if _, err := setupMounts(taskDir, []*drivers.MountConfig{{HostPath: other, TaskPath: "data"}}); err != nil {
		t.Fatal(err)
	}
	if cur, _ := os.Readlink(filepath.Join(taskDir, "data")); cur != other {
		t.Errorf("symlink = %q, want %q", cur, other)
	}

	// Empty dir is replaced; non-empty dir is refused.
	if err := os.MkdirAll(filepath.Join(taskDir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := setupMounts(taskDir, []*drivers.MountConfig{{HostPath: host, TaskPath: "empty"}}); err != nil {
		t.Errorf("empty dir should be replaced: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(taskDir, "full"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "full", "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := setupMounts(taskDir, []*drivers.MountConfig{{HostPath: host, TaskPath: "full"}}); err == nil {
		t.Error("non-empty dir should be refused")
	}

	// Escape is refused; nil/empty entries are skipped.
	if _, err := setupMounts(taskDir, []*drivers.MountConfig{{HostPath: host, TaskPath: "../x"}}); err == nil {
		t.Error("escaping path should be refused")
	}
	if _, err := setupMounts(taskDir, []*drivers.MountConfig{nil, {}}); err != nil {
		t.Errorf("empty entries should be skipped: %v", err)
	}
}
