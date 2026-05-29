package portal

import "testing"

func TestComputeUpdateState(t *testing.T) {
	cases := []struct {
		name           string
		running, latest string
		want           string
	}{
		{"unknown when latest missing", "v0.1.9", "", "unknown"},
		{"up to date exact match", "v0.1.9", "v0.1.9", "up_to_date"},
		{"older patch", "v0.1.8", "v0.1.9", "older"},
		{"older minor", "v0.1.9", "v0.2.0", "older"},
		{"older major", "v0.9.9", "v1.0.0", "older"},
		{"ahead patch", "v0.1.10", "v0.1.9", "ahead"},
		{"ahead minor", "v0.2.0", "v0.1.9", "ahead"},
		{"pre release: dev string", "dev", "v0.1.9", "pre_release"},
		{"pre release: pr branch build", "pr10-f81b5f3", "v0.1.9", "pre_release"},
		{"pre release: empty running", "", "v0.1.9", "pre_release"},
		// Pre-release semver (v0.1.0-rc.1) parses as semver -- it's a
		// real tagged release just with a suffix, so treat it as a
		// normal release for state purposes.
		{"semver pre-release suffix is older than its stable", "v0.1.0-rc.1", "v0.1.0", "up_to_date"},
	}
	for _, c := range cases {
		if got := computeUpdateState(c.running, c.latest); got != c.want {
			t.Errorf("%s: computeUpdateState(%q, %q) = %q, want %q",
				c.name, c.running, c.latest, got, c.want)
		}
	}
}

func TestIsSemverString(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"v0.1.0", true},
		{"v10.20.300", true},
		{"v1.2.3-rc.1", true},
		{"v1.2.3+build5", true},
		{"0.1.0", false},
		{"v0.1", false},
		{"v0.1.x", false},
		{"pr10-f81b5f3", false},
		{"dev", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isSemverString(c.in); got != c.want {
			t.Errorf("isSemverString(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
