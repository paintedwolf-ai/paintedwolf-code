package configdir

import "testing"

func TestReleaseChannelIgnoresDevelopmentEnvironment(t *testing.T) {
	for _, value := range []string{"1", "true", "yes"} {
		if got := channelDirNameForBuild(true, value); got != DirNameProd {
			t.Fatalf("release channel with LYCAON_DEV=%q = %q, want %q", value, got, DirNameProd)
		}
	}
	if got := channelDirNameForBuild(false, "1"); got != DirNameDev {
		t.Fatalf("development channel = %q, want %q", got, DirNameDev)
	}
	if got := configDirOverrideForBuild(true, false, "/tmp/redirected"); got != "" {
		t.Fatalf("release config override = %q, want ignored", got)
	}
	if got := configDirOverrideForBuild(false, false, " /tmp/redirected "); got != "/tmp/redirected" {
		t.Fatalf("development config override = %q", got)
	}
	if got := configDirOverrideForBuild(true, true, " /tmp/isolated "); got != "/tmp/isolated" {
		t.Fatalf("performance config override = %q", got)
	}
}
