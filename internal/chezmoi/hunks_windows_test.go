//go:build windows

package chezmoi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsReplacementFaultRestoresOrPreservesRecovery(t *testing.T) {
	for _, scenario := range []string{"restore", "concurrent", "same-content-new-identity"} {
		t.Run(scenario, func(t *testing.T) {
			concurrent := scenario != "restore"
			concurrentBytes := "concurrent edit\n"
			if scenario == "same-content-new-identity" {
				concurrentBytes = "current\n"
			}
			f, e := hunkFixture(t, "source\n", "current\n")
			snapshot := requireHunks(t, f.s, e)
			original := callReplaceFile
			t.Cleanup(func() { callReplaceFile = original })
			callReplaceFile = func(destination, replacement, backup *uint16) (bool, error) {
				dest, rollback := windows.UTF16PtrToString(destination), windows.UTF16PtrToString(backup)
				if err := os.Rename(dest, rollback); err != nil {
					return false, err
				}
				if concurrent {
					if err := os.WriteFile(dest, []byte(concurrentBytes), 0600); err != nil {
						return false, err
					}
				}
				return false, syscall.Errno(1176)
			}
			_, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current")
			if err == nil {
				t.Fatal("injected replacement failure reported success")
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(e.Target), ".lazychezmoi-*"))
			if concurrent {
				requireContent(t, e.Target, concurrentBytes)
				var partial *replacementFailure
				if !errors.As(err, &partial) || !strings.Contains(err.Error(), "Recovery paths") {
					t.Fatalf("missing recovery outcome: %v", err)
				}
				if len(files) != 2 {
					t.Fatalf("recovery artifacts were discarded: %v", files)
				}
				for _, path := range files {
					if strings.Contains(path, "rollback") {
						requireContent(t, path, "current\n")
					} else {
						requireContent(t, path, "source\n")
					}
				}
			} else {
				requireContent(t, e.Target, "current\n")
				if len(files) != 0 {
					t.Fatalf("restored failure left unnecessary recovery files: %v", files)
				}
			}
		})
	}
}

// Exercise the metadata transfer directly, without chezmoi or shell fixtures.
// Diagnostics deliberately omit paths and SIDs; they identify the exact field
// that differs rather than weakening the post-replacement safety check.
func TestWindowsReplacementPreservesMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := captureFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	securityControl := func(value string) windows.SECURITY_DESCRIPTOR_CONTROL {
		t.Helper()
		sd, err := windows.SecurityDescriptorFromString(value)
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := sd.Control()
		if err != nil {
			t.Fatal(err)
		}
		return control
	}
	logComparison := func(label string, current *fileSnapshot) {
		t.Helper()
		t.Logf("%s: security_equal=%t security_control=%#x/%#x attributes=%#x/%#x creation_equal=%t creation_delta_ns=%d mode=%#o/%#o",
			label, before.metadata.security == current.metadata.security,
			securityControl(before.metadata.security), securityControl(current.metadata.security),
			before.metadata.attributes, current.metadata.attributes,
			before.metadata.creation == current.metadata.creation,
			current.metadata.creation-before.metadata.creation,
			before.info.Mode().Perm(), current.info.Mode().Perm())
	}
	temp, err := prepareReplacement(ctx, before, []byte("after\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(temp) })
	staged, err := captureFile(ctx, temp)
	if err != nil {
		t.Fatal(err)
	}
	logComparison("prepared", staged)
	if err := commitReplacement(temp, before); err != nil {
		t.Fatal(err)
	}
	after, err := captureFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	logComparison("committed", after)
	if string(after.data) != "after\n" || !equivalentMetadata(before.metadata, after.metadata) || before.info.Mode().Perm() != after.info.Mode().Perm() {
		t.Fatal("replacement did not preserve the content/metadata contract")
	}
}
