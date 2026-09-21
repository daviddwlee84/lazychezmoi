//go:build darwin || linux

package chezmoi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestHunkPreservesReceiverXattrsAndRefusesChangedMetadata(t *testing.T) {
	f, e := hunkFixture(t, "source\n", "current\n")
	name := "user.lazychezmoi-test"
	if runtime.GOOS == "darwin" {
		name = "com.lazychezmoi.test"
	}
	if err := unix.Setxattr(e.Source, name, []byte("source metadata"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip("filesystem lacks xattrs")
		}
		t.Fatal(err)
	}
	if err := unix.Setxattr(e.Target, name, []byte("current metadata"), 0); err != nil {
		t.Fatal(err)
	}
	snapshot := requireHunks(t, f.s, e)
	receipt, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(e.Target)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := readXattrs(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(attrs[name], []byte("current metadata")) {
		t.Fatal("copy adopted donor xattrs instead of preserving receiver")
	}
	if err := f.s.UndoCopy(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	snapshot = requireHunks(t, f.s, e)
	if err := unix.Setxattr(e.Target, name, []byte("metadata changed externally"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current"); err == nil {
		t.Fatal("stale xattrs accepted")
	}
	requireContent(t, e.Target, "current\n")
}

func TestHunkRespectsEffectiveOwnerWritePermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses owner write permission")
	}
	f, e := hunkFixture(t, "source\n", "current\n")
	if err := os.Chmod(e.Target, 0420); err != nil {
		t.Fatal(err)
	}
	snapshot := requireHunks(t, f.s, e)
	if _, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current"); err == nil {
		t.Fatal("group-write bit bypassed effective owner read-only permission")
	}
	requireContent(t, e.Target, "current\n")
}
