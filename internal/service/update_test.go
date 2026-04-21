package service

import (
	"testing"
)

func TestInferChannelFromVersion(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{"v0.1.0", "stable"},
		{"v1.2.3", "stable"},
		{"0.1.0", "stable"},
		{"v0.1.0-alpha.1", "alpha"},
		{"v0.1.0-alpha.8", "alpha"},
		{"v0.1.0-beta.1", "beta"},
		{"v0.1.0-beta.3", "beta"},
		{"v0.1.0-rc.1", "alpha"},  // unknown pre-release → alpha (most permissive)
		{"V0.1.0-ALPHA.1", "alpha"}, // case-insensitive
		{"dev", "stable"},          // no dash → stable
		{"", "stable"},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := inferChannelFromVersion(tt.version)
			if got != tt.want {
				t.Errorf("inferChannelFromVersion(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestParseSemver(t *testing.T) {
	tests := []struct {
		tag  string
		want *semverParts
	}{
		{"v1.2.3", &semverParts{1, 2, 3, ""}},
		{"1.2.3", &semverParts{1, 2, 3, ""}},
		{"v0.1.0-alpha.5", &semverParts{0, 1, 0, "alpha.5"}},
		{"v0.1.0-beta.2", &semverParts{0, 1, 0, "beta.2"}},
		{"v0.1.0-alpha-5", &semverParts{0, 1, 0, "alpha.5"}}, // dash → dot normalization
		{"invalid", nil},
		{"1.2", nil},        // missing patch
		{"1.2.x", nil},     // non-numeric patch
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			got := parseSemver(tt.tag)
			if tt.want == nil {
				if got != nil {
					t.Errorf("parseSemver(%q) = %+v, want nil", tt.tag, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseSemver(%q) = nil, want %+v", tt.tag, tt.want)
			}
			if *got != *tt.want {
				t.Errorf("parseSemver(%q) = %+v, want %+v", tt.tag, *got, *tt.want)
			}
		})
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		// major/minor/patch ordering
		{"v1.0.0", "v0.9.9", 1},
		{"v0.9.9", "v1.0.0", -1},
		{"v0.2.0", "v0.1.9", 1},
		{"v0.1.9", "v0.2.0", -1},
		{"v0.1.1", "v0.1.0", 1},
		{"v0.1.0", "v0.1.1", -1},
		{"v1.2.3", "v1.2.3", 0},
		// stable > prerelease for equal X.Y.Z
		{"v0.1.0", "v0.1.0-alpha.1", 1},
		{"v0.1.0-alpha.1", "v0.1.0", -1},
		// prerelease ordering
		{"v0.1.0-beta.1", "v0.1.0-alpha.5", 1},
		{"v0.1.0-alpha.5", "v0.1.0-beta.1", -1},
		{"v0.1.0-alpha.2", "v0.1.0-alpha.1", 1},
		{"v0.1.0-alpha.1", "v0.1.0-alpha.2", -1},
		{"v0.1.0-alpha.1", "v0.1.0-alpha.1", 0},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			a := parseSemver(tt.a)
			b := parseSemver(tt.b)
			if a == nil || b == nil {
				t.Fatalf("parseSemver failed for inputs")
			}
			got := compareSemver(*a, *b)
			if got != tt.want {
				t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"v0.1.1", "v0.1.0", true},
		{"v0.1.0", "v0.1.1", false},
		{"v0.1.0", "v0.1.0", false},
		{"v0.2.0", "v0.1.9", true},
		{"v1.0.0", "v0.9.9", true},
		// stable is newer than same-version prerelease
		{"v0.1.0", "v0.1.0-alpha.5", true},
		{"v0.1.0-alpha.5", "v0.1.0", false},
		// beta is newer than alpha of same version
		{"v0.1.0-beta.1", "v0.1.0-alpha.8", true},
		{"v0.1.0-alpha.8", "v0.1.0-beta.1", false},
		// alpha increments
		{"v0.1.0-alpha.2", "v0.1.0-alpha.1", true},
		{"v0.1.0-alpha.1", "v0.1.0-alpha.2", false},
		// invalid tags fall back to string inequality
		{"invalid-new", "invalid-old", true},
		{"same", "same", false},
	}

	for _, tt := range tests {
		t.Run(tt.latest+"_vs_"+tt.current, func(t *testing.T) {
			got := isNewer(tt.latest, tt.current)
			if got != tt.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

func TestReleaseMatchesChannel(t *testing.T) {
	makeRelease := func(tag string, prerelease, draft bool) *githubRelease {
		return &githubRelease{TagName: tag, Prerelease: prerelease, Draft: draft}
	}

	tests := []struct {
		name    string
		release *githubRelease
		channel string
		want    bool
	}{
		// drafts always excluded
		{"draft excluded", makeRelease("v1.0.0", false, true), "alpha", false},
		{"draft excluded stable", makeRelease("v1.0.0", false, true), "stable", false},
		// empty tag excluded
		{"empty tag", makeRelease("", false, false), "alpha", false},
		// stable channel: only stable releases
		{"stable on stable", makeRelease("v1.0.0", false, false), "stable", true},
		{"alpha on stable", makeRelease("v0.1.0-alpha.1", true, false), "stable", false},
		{"beta on stable", makeRelease("v0.1.0-beta.1", true, false), "stable", false},
		// beta channel: stable + beta, no alpha
		{"stable on beta", makeRelease("v1.0.0", false, false), "beta", true},
		{"beta on beta", makeRelease("v0.1.0-beta.1", true, false), "beta", true},
		{"alpha on beta", makeRelease("v0.1.0-alpha.1", true, false), "beta", false},
		// alpha channel: everything non-draft
		{"stable on alpha", makeRelease("v1.0.0", false, false), "alpha", true},
		{"beta on alpha", makeRelease("v0.1.0-beta.1", true, false), "alpha", true},
		{"alpha on alpha", makeRelease("v0.1.0-alpha.5", true, false), "alpha", true},
		// invalid semver tag excluded
		{"invalid tag", makeRelease("nightly-20240101", false, false), "alpha", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := releaseMatchesChannel(tt.release, tt.channel)
			if got != tt.want {
				t.Errorf("releaseMatchesChannel(%q, %q) = %v, want %v", tt.release.TagName, tt.channel, got, tt.want)
			}
		})
	}
}

func TestComparePrerelease(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"alpha.1", "alpha.1", 0},
		{"alpha.2", "alpha.1", 1},
		{"alpha.1", "alpha.2", -1},
		{"beta.1", "alpha.5", 1},  // "beta" > "alpha" lexicographically
		{"alpha.5", "beta.1", -1},
		// numeric identifiers compare numerically not lexicographically
		{"alpha.10", "alpha.9", 1},
		{"alpha.9", "alpha.10", -1},
		// more identifiers = newer (semver spec)
		{"alpha.1.1", "alpha.1", 1},
		{"alpha.1", "alpha.1.1", -1},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := comparePrerelease(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("comparePrerelease(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
