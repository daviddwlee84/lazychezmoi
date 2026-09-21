//go:build darwin

package chezmoi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinHunkPreservesACL(t *testing.T) {
	f, e := hunkFixture(t, "source\n", "current\n")
	cmd := exec.Command("/bin/chmod", "+a", "everyone allow read", e.Target)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture ACL: %v %s", err, data)
	}
	acl := func() string {
		t.Helper()
		data, err := exec.Command("/bin/ls", "-lde", e.Target).Output()
		if err != nil {
			t.Fatal(err)
		}
		_, rest, _ := strings.Cut(string(data), "\n")
		return rest
	}
	before := acl()
	if !strings.Contains(before, "everyone allow read") {
		t.Fatalf("ACL not installed: %s", before)
	}
	snapshot := requireHunks(t, f.s, e)
	receipt, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current")
	if err != nil {
		t.Fatal(err)
	}
	if after := acl(); after != before {
		t.Fatalf("ACL changed: %q -> %q", before, after)
	}
	if err := f.s.UndoCopy(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if after := acl(); after != before {
		t.Fatalf("undo changed ACL: %q -> %q", before, after)
	}
}

func TestDarwinHunkDoesNotBypassDenyACL(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root permission override")
	}
	f, e := hunkFixture(t, "source\n", "current\n")
	cmd := exec.Command("/bin/chmod", "+a", "everyone deny write", e.Target)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture ACL: %v %s", err, data)
	}
	snapshot := requireHunks(t, f.s, e)
	if _, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current"); err == nil {
		t.Fatal("deny-write ACL bypassed")
	}
	requireContent(t, e.Target, "current\n")
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(e.Target), ".lazychezmoi-*"))
	if len(leftovers) != 0 {
		t.Fatalf("failed copy leaked temp files: %v", leftovers)
	}
}
