//go:build darwin

package chezmoi

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

func statMetadata(f *os.File) (fileMetadata, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return fileMetadata{}, err
	}
	// Compressed, tracked, immutable, and other special file flags cannot be
	// silently flattened by a text-copy operation.
	if stat.Flags & ^uint32(unix.UF_HIDDEN|unix.UF_NODUMP) != 0 {
		return fileMetadata{}, errors.New("file has unsupported macOS flags; use its native editor")
	}
	security, err := darwinSecurity(f)
	if err != nil {
		return fileMetadata{}, err
	}
	return fileMetadata{identity: fmt.Sprintf("%d/%d", stat.Dev, stat.Ino), changed: fmt.Sprintf("%d/%d", stat.Ctim.Sec, stat.Ctim.Nsec), uid: int(stat.Uid), gid: int(stat.Gid), links: uint64(stat.Nlink), flags: stat.Flags, security: security}, nil
}

// attrlist and attrreference match macOS sys/attr.h. The security payload is
// treated as an opaque, bounds-checked kauth_filesec blob; no ACL entries or
// private flags are translated or normalized.
type darwinAttrlist struct {
	count, reserved                       uint16
	common, volume, directory, file, fork uint32
}

func darwinSecurity(f *os.File) ([]byte, error) {
	attrs := darwinAttrlist{count: 5, common: unix.ATTR_CMN_EXTENDED_SECURITY}
	buffer := make([]byte, 8192)
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, f.Fd(), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	runtime.KeepAlive(f)
	runtime.KeepAlive(attrs)
	runtime.KeepAlive(buffer)
	if errno != 0 {
		return nil, fmt.Errorf("cannot inspect macOS ACL safely: %w", errno)
	}
	total := int(binary.LittleEndian.Uint32(buffer[:4]))
	offset := int(int32(binary.LittleEndian.Uint32(buffer[4:8])))
	size := int(binary.LittleEndian.Uint32(buffer[8:12]))
	start := 4 + offset
	if total == 12 && offset == 8 && size == 0 {
		return nil, nil
	}
	if total < 12 || total > len(buffer) || offset < 8 || size < 44 || start > total || size > total-start {
		return nil, fmt.Errorf("unrecognized macOS security attributes (length %d, offset %d, security size %d); no hunk copy allowed", total, offset, size)
	}
	security := append([]byte(nil), buffer[start:start+size]...)
	if binary.LittleEndian.Uint32(security[:4]) != 0x012cc16d {
		return nil, errors.New("unrecognized macOS security magic")
	}
	count := binary.LittleEndian.Uint32(security[36:40])
	if count != ^uint32(0) && (count > 128 || 44+int(count)*24 != len(security)) {
		return nil, errors.New("unrecognized macOS ACL size")
	}
	if binary.LittleEndian.Uint32(security[40:44])&(1<<16) != 0 {
		return nil, errors.New("macOS ACL has deferred inheritance; hunk replacement cannot preserve it safely")
	}
	return security, nil
}

func preserveSecurity(f *os.File, metadata fileMetadata) error {
	security := metadata.security
	if len(security) == 0 {
		// KAUTH_FILESEC_NOACL removes inherited ACLs from the replacement.
		security = make([]byte, 44)
		binary.LittleEndian.PutUint32(security[:4], 0x012cc16d)
		binary.LittleEndian.PutUint32(security[36:40], ^uint32(0))
	}
	attrs := darwinAttrlist{count: 5, common: unix.ATTR_CMN_EXTENDED_SECURITY}
	buffer := make([]byte, 8+len(security))
	binary.LittleEndian.PutUint32(buffer[:4], 8)
	binary.LittleEndian.PutUint32(buffer[4:8], uint32(len(security)))
	copy(buffer[8:], security)
	_, _, errno := unix.Syscall6(unix.SYS_FSETATTRLIST, f.Fd(), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	runtime.KeepAlive(f)
	runtime.KeepAlive(attrs)
	runtime.KeepAlive(buffer)
	if errno != 0 {
		return fmt.Errorf("cannot preserve macOS ACL; no copy performed: %w", errno)
	}
	return unix.Fchflags(int(f.Fd()), int(metadata.flags))
}

func prepareReplacement(ctx context.Context, original *fileSnapshot, data []byte) (name string, err error) {
	// Empty 0600 scratch receives the original ACL before any content. A
	// clonefile staging copy would expose original bytes before ACL restoration.
	f, err := os.CreateTemp(filepath.Dir(original.path), ".lazychezmoi-copy-*")
	if err != nil {
		return "", err
	}
	name = f.Name()
	cleanupName := name
	defer func() {
		if err != nil {
			_ = os.Remove(cleanupName)
		}
	}()
	err = populateReplacement(ctx, f, original, data)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return name, err
}
