package steamcmd

import "testing"

func TestResolveLaunchPath(t *testing.T) {
	cases := []struct {
		name       string
		command    string
		installDir string
		want       string
	}{
		{
			name:       "relative path with directory component resolves against installDir",
			command:    "local/steamapp/PalServer.sh",
			installDir: "/alloc/task/local/steamapp",
			want:       "/alloc/task/local/steamapp/local/steamapp/PalServer.sh",
		},
		{
			name:       "bare command name is left alone for PATH lookup",
			command:    "xvfb-run",
			installDir: "/alloc/task/local/steamapp",
			want:       "xvfb-run",
		},
		{
			name:       "bare command name 'wine' is left alone for PATH lookup",
			command:    "wine",
			installDir: "/alloc/task/local/steamapp",
			want:       "wine",
		},
		{
			name:       "already-absolute path is left alone",
			command:    "/usr/bin/wine",
			installDir: "/alloc/task/local/steamapp",
			want:       "/usr/bin/wine",
		},
		{
			name:       "relative path directly under installDir",
			command:    "PalServer.sh",
			installDir: "/alloc/task/local/steamapp",
			want:       "PalServer.sh", // no "/" in command -> bare name, PATH lookup
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLaunchPath(tc.command, tc.installDir)
			if got != tc.want {
				t.Errorf("resolveLaunchPath(%q, %q) = %q, want %q", tc.command, tc.installDir, got, tc.want)
			}
		})
	}
}
