package chezmoi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func hunkFixture(t *testing.T, source, current string) (fixture, Entry) {
	t.Helper()
	f := newFixture(t)
	f.write(t, "config.txt", source)
	if err := os.WriteFile(filepath.Join(f.destination, "config.txt"), []byte(current), 0640); err != nil {
		t.Fatal(err)
	}
	return f, requireEntry(t, f.s, "config.txt")
}
func requireHunks(t *testing.T, s *Service, e Entry) *DiffSnapshot {
	t.Helper()
	snapshot, err := s.Hunks(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func requireContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("content %s: %q, %v; want %q", filepath.Base(path), data, err, want)
	}
}

func TestHunkCopyBothDirectionsAndUndo(t *testing.T) {
	for _, direction := range []string{"source-to-current", "current-to-source"} {
		t.Run(direction, func(t *testing.T) {
			f, e := hunkFixture(t, "head\nsource\ntail\n", "head\ncurrent\ntail\n")
			snapshot := requireHunks(t, f.s, e)
			if len(snapshot.Hunks) != 1 || !strings.Contains(snapshot.Raw, "-current\n+source\n") {
				t.Fatalf("incorrect diff: %+v", snapshot.Hunks)
			}
			startSource, _ := os.ReadFile(e.Source)
			startCurrent, _ := os.ReadFile(e.Target)
			receipt, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, direction)
			if err != nil {
				t.Fatal(err)
			}
			if direction == "source-to-current" {
				requireContent(t, e.Target, string(startSource))
				requireContent(t, e.Source, string(startSource))
			} else {
				requireContent(t, e.Source, string(startCurrent))
				requireContent(t, e.Target, string(startCurrent))
			}
			info, err := os.Stat(receipt.Path)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" {
				want := os.FileMode(0640)
				if direction == "current-to-source" {
					want = 0600
				}
				if info.Mode().Perm() != want {
					t.Fatalf("mode changed: %o want %o", info.Mode().Perm(), want)
				}
			}
			if err := f.s.UndoCopy(context.Background(), receipt); err != nil {
				t.Fatal(err)
			}
			requireContent(t, e.Source, string(startSource))
			requireContent(t, e.Target, string(startCurrent))
			if err := f.s.UndoCopy(context.Background(), receipt); err == nil {
				t.Fatal("receipt reused")
			}
		})
	}
}

func TestHunkCopyOnlySelectedRangeAndKeepsNativeState(t *testing.T) {
	base := "old one\n1\n2\n3\n4\n5\n6\n7\nold two\n"
	f, e := hunkFixture(t, base, base)
	runOperation(t, f.s, Operation{Kind: "apply", Targets: []string{e.Target}, ExcludeScripts: true})
	stateBefore, err := f.s.read(context.Background(), "state", "get", "--bucket=entryState", "--key", e.Target)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "config.txt", strings.ReplaceAll(base, "old", "new"))
	snapshot := requireHunks(t, f.s, e)
	if len(snapshot.Hunks) != 2 {
		t.Fatalf("expected separated hunks: %+v", snapshot.Hunks)
	}
	_, err = f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current")
	if err != nil {
		t.Fatal(err)
	}
	requireContent(t, e.Target, strings.Replace(base, "old one", "new one", 1))
	stateAfter, err := f.s.read(context.Background(), "state", "get", "--bucket=entryState", "--key", e.Target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("hunk copy changed chezmoi persistent state")
	}
	entries, err := f.s.Entries(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Drift != "M" || entries[0].Pending != "M" {
		t.Fatalf("partial copy should remain drift/pending: %+v", entries)
	}
}

func TestHunkPreservesRawTerminatorsAndBOM(t *testing.T) {
	for _, pair := range [][2]string{{"alpha\r\nsource\r\n", "alpha\r\ncurrent\r\n"}, {"same\r\n", "same\n"}, {"alpha\nsource", "alpha\ncurrent\n"}, {"\ufeffsource\r\nend", "current\nend\n"}, {"start\nadd\nend\n", "start\nend\n"}, {"start\nend\n", "start\nremove\nend\n"}} {
		t.Run(digest(pair[0], pair[1])[:8], func(t *testing.T) {
			f, e := hunkFixture(t, pair[0], pair[1])
			snapshot := requireHunks(t, f.s, e)
			if len(snapshot.Hunks) != 1 {
				t.Fatalf("expected one raw hunk: %+v", snapshot.Hunks)
			}
			_, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current")
			if err != nil {
				t.Fatal(err)
			}
			requireContent(t, e.Target, pair[0])
		})
	}
}

func TestHunkRefusesStaleSnapshotsAndUndo(t *testing.T) {
	for _, side := range []string{"source", "current", "metadata", "identity"} {
		t.Run(side, func(t *testing.T) {
			f, e := hunkFixture(t, "source\n", "current\n")
			snapshot := requireHunks(t, f.s, e)
			switch side {
			case "source":
				f.write(t, "config.txt", "external edit\n")
			case "current":
				if err := os.WriteFile(e.Target, []byte("external edit\n"), 0640); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				if err := os.Chmod(e.Target, 0600); err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS == "windows" {
					if err := os.Chmod(e.Target, 0444); err != nil {
						t.Fatal(err)
					}
				}
			case "identity":
				replacement := e.Target + ".replace"
				if err := os.WriteFile(replacement, []byte("current\n"), 0640); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, e.Target); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current"); err == nil {
				t.Fatal("stale snapshot accepted")
			}
		})
	}
	f, e := hunkFixture(t, "source\n", "current\n")
	snapshot := requireHunks(t, f.s, e)
	receipt, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.Target, []byte("changed after copy\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := f.s.UndoCopy(context.Background(), receipt); err == nil {
		t.Fatal("undo clobbered external edit")
	}
	requireContent(t, e.Target, "changed after copy\n")
}

func TestHunkEligibilityAndPublicSnapshotCannotAuthorizeWrites(t *testing.T) {
	f, e := hunkFixture(t, "source\n", "current\n")
	for _, kind := range []string{"create", "modify", "symlink", "remove", "script", "script-once"} {
		changed := e
		changed.Kind = kind
		if _, err := f.s.Hunks(context.Background(), changed); err == nil {
			t.Fatalf("accepted %s", kind)
		}
	}
	for _, field := range []string{"template", "encrypted"} {
		changed := e
		if field == "template" {
			changed.Template = true
		} else {
			changed.Encrypted = true
		}
		if _, err := f.s.Hunks(context.Background(), changed); err == nil {
			t.Fatalf("accepted %s", field)
		}
	}
	snapshot := requireHunks(t, f.s, e)
	body, _ := json.Marshal(snapshot)
	var imported DiffSnapshot
	if err := json.Unmarshal(body, &imported); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CopyHunk(context.Background(), &imported, snapshot.Hunks[0].ID, "source-to-current"); err == nil {
		t.Fatal("JSON snapshot authorized write without private byte snapshots")
	}
	if _, err := f.s.CopyHunk(context.Background(), snapshot, "wrong-id", "source-to-current"); err == nil {
		t.Fatal("unknown hunk ID accepted")
	}
	if err := os.Remove(e.Target); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Hunks(context.Background(), e); err == nil {
		t.Fatal("missing-file creation accepted")
	}
}

func TestHunkRefusesSymlinkHardlinkBinaryAndEmptySource(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "binary", "invalid-utf8", "empty-source", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			f, e := hunkFixture(t, "source\n", "current\n")
			switch kind {
			case "symlink":
				if err := os.Remove(e.Target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(e.Source, e.Target); err != nil {
					if runtime.GOOS == "windows" {
						t.Skip("symlink privilege unavailable")
					}
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(e.Target, e.Target+".link"); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
			case "binary":
				if err := os.WriteFile(e.Target, []byte{'x', 0, 'y'}, 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-utf8":
				if err := os.WriteFile(e.Target, []byte{0xff, 0xfe}, 0600); err != nil {
					t.Fatal(err)
				}
			case "empty-source":
				f.write(t, "config.txt", " \n")
			case "oversized":
				f.write(t, "config.txt", strings.Repeat("x", maxHunkBytes+1))
			}
			if _, err := f.s.Hunks(context.Background(), e); err == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}

func TestHunkEmptyResultAndReadonlyRefusal(t *testing.T) {
	f, e := hunkFixture(t, "source\n", "")
	snapshot := requireHunks(t, f.s, e)
	if _, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "current-to-source"); err == nil {
		t.Fatal("empty source removal accepted")
	}
	if err := os.Chmod(e.Target, 0444); err != nil {
		t.Fatal(err)
	}
	snapshot = requireHunks(t, f.s, e)
	if _, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current"); err == nil {
		t.Fatal("readonly target bypassed")
	}
}

func TestHunkWorkBoundsTrimUnchangedContextAndCancel(t *testing.T) {
	prefix := strings.Repeat("unchanged\n", 15000)
	suffix := strings.Repeat("after\n", 15000)
	f, e := hunkFixture(t, prefix+"source\n"+suffix, prefix+"current\n"+suffix)
	snapshot := requireHunks(t, f.s, e)
	if len(snapshot.Hunks) != 1 {
		t.Fatalf("long file small edit rejected: %+v", snapshot.Hunks)
	}
	f.write(t, "config.txt", strings.Repeat("a\nb\n", 2000))
	if err := os.WriteFile(e.Target, []byte(strings.Repeat("b\na\n", 2000)), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := f.s.Hunks(context.Background(), e); err == nil || !strings.Contains(err.Error(), "bounded") {
		t.Fatalf("pathological matcher unbounded: %v", err)
	}
	if time.Since(start) > time.Second*2 {
		t.Fatal("work bound checked too late")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.s.Hunks(ctx, e); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled diff: %v", err)
	}
	if _, err := f.s.CopyHunk(ctx, snapshot, snapshot.Hunks[0].ID, "source-to-current"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled copy: %v", err)
	}
}

func TestExactAncestorBlocksNativeReaddButHunkStaysScoped(t *testing.T) {
	f := newFixture(t)
	path := f.write(t, "exact_dot_config/file.txt", "source\n")
	f.write(t, "exact_dot_config/sibling.txt", "tracked sibling\n")
	if err := os.MkdirAll(filepath.Join(f.destination, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.destination, ".config/file.txt"), []byte("current\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.destination, ".config/unmanaged.txt"), []byte("unmanaged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e := requireEntry(t, f.s, path)
	if !e.ExactAncestor {
		t.Fatal("exact ancestor not detected")
	}
	if _, err := f.s.Command(context.Background(), Operation{Kind: "re-add", Targets: []string{e.Target}}); err == nil {
		t.Fatal("native re-add accepted below exact_")
	}
	snapshot := requireHunks(t, f.s, e)
	if _, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "current-to-source"); err != nil {
		t.Fatal(err)
	}
	requireContent(t, path, "current\n")
	requireContent(t, filepath.Join(f.source, "exact_dot_config/sibling.txt"), "tracked sibling\n")
	if _, err := os.Stat(filepath.Join(f.source, "exact_dot_config/unmanaged.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unmanaged sibling imported")
	}
}

func TestNativeDiffDirectionCannotBeReversedByConfig(t *testing.T) {
	f, e := hunkFixture(t, "source\n", "current\n")
	config, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	config = bytes.Replace(config, []byte("[diff]\n"), []byte("[diff]\nreverse = true\n"), 1)
	if err := os.WriteFile(f.config, config, 0600); err != nil {
		t.Fatal(err)
	}
	diff, err := f.s.Preview(context.Background(), e, "diff")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-current") || !strings.Contains(diff, "+source") {
		t.Fatalf("native diff was reversed: %s", diff)
	}
}

func TestHunkWriteAndUndoDoNotStartChezmoi(t *testing.T) {
	f, e := hunkFixture(t, "source\n", "current\n")
	snapshot := requireHunks(t, f.s, e)
	f.s.opts.Binary = "nonexistent-backend-write-must-not-start-subprocess"
	receipt, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.UndoCopy(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	requireContent(t, e.Target, "current\n")
}

func TestHunkSourceControlsAndDirectoryMappingInvalidateSnapshots(t *testing.T) {
	for _, control := range []string{"config", "ignore", "directory"} {
		t.Run(control, func(t *testing.T) {
			f, e := hunkFixture(t, "source\n", "current\n")
			snapshot := requireHunks(t, f.s, e)
			switch control {
			case "config":
				body, err := os.ReadFile(f.config)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.config, append(body, []byte("\n# context changed\n")...), 0600); err != nil {
					t.Fatal(err)
				}
			case "ignore":
				f.write(t, ".chezmoiignore", "config.txt\n")
			case "directory":
				old := f.source + ".moved"
				if err := os.Rename(f.source, old); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.source, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(old, "config.txt"), e.Source); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current"); err == nil {
				t.Fatal("changed context accepted")
			}
			requireContent(t, e.Target, "current\n")
		})
	}
}
