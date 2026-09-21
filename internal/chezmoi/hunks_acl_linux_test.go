//go:build linux

package chezmoi

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxHunkPreservesPOSIXACL(t *testing.T) {
	f, e := hunkFixture(t, "source\n", "current\n")
	var acl bytes.Buffer
	_ = binary.Write(&acl, binary.LittleEndian, uint32(2))
	for _, v := range []struct {
		tag, perm uint16
		id        uint32
	}{{1, 6, ^uint32(0)}, {2, 4, 65534}, {4, 4, ^uint32(0)}, {16, 4, ^uint32(0)}, {32, 0, ^uint32(0)}} {
		_ = binary.Write(&acl, binary.LittleEndian, v.tag)
		_ = binary.Write(&acl, binary.LittleEndian, v.perm)
		_ = binary.Write(&acl, binary.LittleEndian, v.id)
	}
	if err := unix.Setxattr(e.Target, "system.posix_acl_access", acl.Bytes(), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip("filesystem lacks POSIX ACL support")
		}
		t.Fatal(err)
	}
	snapshot := requireHunks(t, f.s, e)
	_, err := f.s.CopyHunk(context.Background(), snapshot, snapshot.Hunks[0].ID, "source-to-current")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4096)
	n, err := unix.Getxattr(e.Target, "system.posix_acl_access", data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data[:n], acl.Bytes()) {
		t.Fatal("POSIX ACL changed")
	}
}
