package main

import (
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
)

func TestProgressRendererVisibleRecentLinesKeepsActiveLineInContext(t *testing.T) {
	t.Parallel()

	var renderer progressRenderer
	for i := 1; i <= 7; i++ {
		renderer.appendCommittedLine("line " + strconv.Itoa(i))
	}
	renderer.printOutput("active", true)

	got := renderer.visibleRecentLines()
	if len(got) != 8 {
		t.Fatalf("visible line count mismatch: got %d want 8", len(got))
	}
	if got[0] != "line 1" || got[6] != "line 7" || got[7] != "active" {
		t.Fatalf("visible lines mismatch: got %#v", got)
	}
}

func TestProgressRendererVisibleRecentLinesRollsForward(t *testing.T) {
	t.Parallel()

	var renderer progressRenderer
	for i := 1; i <= 8; i++ {
		renderer.appendCommittedLine("line " + strconv.Itoa(i))
	}
	renderer.printOutput("active", true)

	got := renderer.visibleRecentLines()
	if len(got) != 8 {
		t.Fatalf("visible line count mismatch: got %d want 8", len(got))
	}
	if got[0] != "line 2" || got[6] != "line 8" || got[7] != "active" {
		t.Fatalf("visible lines mismatch: got %#v", got)
	}
}

func TestProgressRendererReplaceEventsScrollWindow(t *testing.T) {
	t.Parallel()

	var renderer progressRenderer
	for i := 1; i <= 7; i++ {
		renderer.appendCommittedLine("line " + strconv.Itoa(i))
	}

	renderer.printOutput("progress 1", true)
	renderer.printOutput("progress 2", true)

	got := renderer.visibleRecentLines()
	if len(got) != 8 {
		t.Fatalf("visible line count mismatch: got %d want 8", len(got))
	}
	if got[0] != "line 2" || got[5] != "line 7" || got[6] != "progress 1" || got[7] != "progress 2" {
		t.Fatalf("visible lines mismatch: got %#v", got)
	}
}

func TestProgressRendererCommitLineDoesNotDuplicateActiveLine(t *testing.T) {
	t.Parallel()

	var renderer progressRenderer
	renderer.printOutput("progress", true)
	renderer.printOutput("progress", false)

	got := renderer.visibleRecentLines()
	if len(got) != 1 {
		t.Fatalf("visible line count mismatch: got %d want 1", len(got))
	}
	if got[0] != "progress" {
		t.Fatalf("visible lines mismatch: got %#v", got)
	}
	if renderer.visibleLineCount() != 1 {
		t.Fatalf("visible line total mismatch: got %d want 1", renderer.visibleLineCount())
	}
}

func TestNormalizeFlagArgsAllowsTrailingBoolFlag(t *testing.T) {
	t.Parallel()

	got, err := normalizeFlagArgs([]string{"debian-docker-lxc", "testbox", "--tslogin"}, map[string]bool{
		"--config":  false,
		"--dry-run": true,
		"--tslogin": true,
	})
	if err != nil {
		t.Fatalf("normalizeFlagArgs returned error: %v", err)
	}

	want := []string{"--tslogin", "debian-docker-lxc", "testbox"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args: got %#v want %#v", got, want)
	}
}

func TestResolveGuestLocaleUsesProfileOverride(t *testing.T) {
	t.Parallel()

	got := resolveGuestLocale(config.ProvisionProfile{Locale: "de_DE.UTF8"})
	if got != "de_DE.UTF-8" {
		t.Fatalf("unexpected locale: %q", got)
	}
}

func TestResolveGuestLocaleFallsBackToEnvironment(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "UTF-8")
	t.Setenv("LANG", "")

	got := resolveGuestLocale(config.ProvisionProfile{})
	if got != "en_US.UTF-8" {
		t.Fatalf("unexpected locale: %q", got)
	}
}

func TestResolveGuestLocaleFallsBackToDefault(t *testing.T) {
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		old, ok := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		defer func(key, old string, ok bool) {
			if ok {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		}(key, old, ok)
	}

	got := resolveGuestLocale(config.ProvisionProfile{})
	if got != "en_US.UTF-8" {
		t.Fatalf("unexpected locale: %q", got)
	}
}
