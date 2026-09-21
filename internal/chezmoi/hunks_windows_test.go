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
