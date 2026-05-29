package main

import "testing"

func TestResolveOneBareFilename(t *testing.T) {
	files := map[string]string{
		"setup.exe":   `C:\Users\autodeploy\work\pkg-1\files\setup.exe`,
		"config.json": `C:\Users\autodeploy\work\pkg-1\files\config.json`,
	}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare filename hits the known-files map",
			in:   "setup.exe",
			want: `C:\Users\autodeploy\work\pkg-1\files\setup.exe`,
		},
		{
			name: "bare filename that wasn't uploaded -- pass through so the operator sees 'not found'",
			in:   "missing.exe",
			want: "missing.exe",
		},
		{
			name: "absolute Windows path stays put",
			in:   `C:\Program Files\Acme\acme.exe`,
			want: `C:\Program Files\Acme\acme.exe`,
		},
		{
			name: "Windows env-var path stays put",
			in:   `%ProgramFiles%\Microsoft VS Code\code.exe`,
			want: `%ProgramFiles%\Microsoft VS Code\code.exe`,
		},
		{
			name: "POSIX absolute (dev / test) stays put",
			in:   "/usr/local/bin/setup",
			want: "/usr/local/bin/setup",
		},
		{
			name: "path with a separator stays put even if the basename matches",
			in:   `C:\Other\setup.exe`,
			want: `C:\Other\setup.exe`,
		},
		{
			name: "empty string is a no-op",
			in:   "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveOne(c.in, files)
			if got != c.want {
				t.Errorf("resolveOne(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
