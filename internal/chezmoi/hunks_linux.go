//go:build linux

package chezmoi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func statMetadata(f *os.File) (fileMetadata, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return fileMetadata{}, err
	}
	flags, err := unix.IoctlGetInt(int(f.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil && !errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.ENOTSUP) {
		return fileMetadata{}, fmt.Errorf("cannot inspect filesystem flags safely: %w", err)
	}
	if err != nil {
		flags = 0
	}
	// The extents storage bit is preserved naturally by same-volume writes.
	// Refuse nodump, append-only, compression and other flags rather than
	// silently discarding them when replacing the inode.
	if uint32(flags) & ^uint32(0x00080000) != 0 {
		return fileMetadata{}, errors.New("file has unsupported Linux filesystem flags; use its native editor")
	}
	return fileMetadata{identity: fmt.Sprintf("%d/%d", stat.Dev, stat.Ino), changed: fmt.Sprintf("%d/%d", stat.Ctim.Sec, stat.Ctim.Nsec), uid: int(stat.Uid), gid: int(stat.Gid), links: uint64(stat.Nlink), flags: uint32(flags)}, nil
}

func preserveSecurity(f *os.File, metadata fileMetadata) error {
	const acl = "system.posix_acl_access"
	if value, ok := metadata.xattrs[acl]; ok {
		return unix.Fsetxattr(int(f.Fd()), acl, value, 0)
	}
	// Remove an inherited default ACL before copying any xattr/content bytes.
	err := unix.Fremovexattr(int(f.Fd()), acl)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	return err
}

func prepareReplacement(ctx context.Context, original *fileSnapshot, data []byte) (name string, err error) {
	f, err := os.CreateTemp(filepath.Dir(original.path), ".lazychezmoi-copy-*")
	if err != nil {
		return "", err
	}
	name = f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	err = populateReplacement(ctx, f, original, data)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return name, err
}
