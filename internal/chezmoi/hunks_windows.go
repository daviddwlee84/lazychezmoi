//go:build windows

package chezmoi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileMetadata struct {
	identity, changed, security string
	attributes                  uint32
	creation                    int64
}

const copySecurity = windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION

func captureMetadata(ctx context.Context, f *os.File, info os.FileInfo) (fileMetadata, error) {
	if err := ctx.Err(); err != nil {
		return fileMetadata{}, err
	}
	var stat windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &stat); err != nil {
		return fileMetadata{}, err
	}
	if stat.NumberOfLinks != 1 {
		return fileMetadata{}, errors.New("hardlinked files cannot use hunk copies")
	}
	allowed := uint32(windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM | windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_ATTRIBUTE_NOT_CONTENT_INDEXED)
	if stat.FileAttributes & ^allowed != 0 {
		return fileMetadata{}, errors.New("file has unsupported Windows attributes or reparse metadata; use its native editor")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, copySecurity)
	if err != nil {
		return fileMetadata{}, fmt.Errorf("cannot inspect file security safely: %w", err)
	}
	return fileMetadata{identity: fmt.Sprintf("%d/%d/%d", stat.VolumeSerialNumber, stat.FileIndexHigh, stat.FileIndexLow), changed: fmt.Sprintf("%d", stat.LastWriteTime.Nanoseconds()), security: sd.String(), attributes: stat.FileAttributes, creation: stat.CreationTime.Nanoseconds()}, nil
}
func metadataFingerprint(m fileMetadata) string {
	return digest(m.identity, m.changed, m.security, fmt.Sprintf("%d/%d", m.attributes, m.creation))
}
func equivalentMetadata(a, b fileMetadata) bool {
	return equivalentSecurity(a.security, b.security) && a.attributes == b.attributes && a.creation == b.creation
}

func equivalentSecurity(a, b string) bool {
	// SetNamedSecurityInfo may set SE_DACL_AUTO_INHERITED while preserving
	// the owner's/group's SIDs and the complete DACL. That control bit records
	// automatic-inheritance bookkeeping, not an access permission. Normalize
	// only that observed bit; ACE flags/order and DACL protection still must
	// match exactly. The raw descriptor remains in metadataFingerprint so a
	// change to an inspected file still invalidates the snapshot.
	// https://learn.microsoft.com/windows/win32/api/aclapi/nf-aclapi-setnamedsecurityinfow
	normalize := func(value string) (string, error) {
		sd, err := windows.SecurityDescriptorFromString(value)
		if err != nil {
			return "", err
		}
		if err := sd.SetControl(windows.SE_DACL_AUTO_INHERITED, 0); err != nil {
			return "", err
		}
		canonical := sd.String()
		if canonical == "" {
			return "", errors.New("cannot serialize file security descriptor")
		}
		return canonical, nil
	}
	left, err := normalize(a)
	if err != nil {
		return false
	}
	right, err := normalize(b)
	return err == nil && left == right
}

func prepareReplacement(ctx context.Context, original *fileSnapshot, data []byte) (name string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Opening without truncation checks the receiving ACL instead of silently
	// relying on the containing directory's replacement permission.
	check, err := os.OpenFile(original.path, os.O_WRONLY, 0)
	if err != nil {
		return "", err
	}
	info, statErr := check.Stat()
	_ = check.Close()
	if statErr != nil {
		return "", statErr
	}
	if !os.SameFile(info, original.info) {
		return "", errors.New("receiving file changed before preparing replacement")
	}
	sd, err := windows.GetNamedSecurityInfo(original.path, windows.SE_FILE_OBJECT, copySecurity)
	if err != nil {
		return "", err
	}
	if sd.String() != original.metadata.security {
		return "", errors.New("file security changed; refresh before copying")
	}
	f, err := os.CreateTemp(filepath.Dir(original.path), ".lazychezmoi-copy-*")
	if err != nil {
		return "", err
	}
	name = f.Name()
	cleanupName := name
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(cleanupName)
		}
	}()
	owner, _, err := sd.Owner()
	if err != nil {
		return "", err
	}
	group, _, err := sd.Group()
	if err != nil {
		return "", err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return "", err
	}
	control, _, err := sd.Control()
	if err != nil {
		return "", err
	}
	securityFlags := windows.SECURITY_INFORMATION(copySecurity)
	if control&windows.SE_DACL_PROTECTED != 0 {
		securityFlags |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		securityFlags |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if err = windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, securityFlags, owner, group, dacl, nil); err != nil {
		return "", fmt.Errorf("cannot preserve owner/ACL; no copy performed: %w", err)
	}
	if _, err = f.Write(data); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	if err = windows.SetFileAttributes(ptr, original.metadata.attributes); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return name, nil
}

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

var callReplaceFile = func(destination, replacement, backup *uint16) (bool, error) {
	ok, _, err := replaceFileW.Call(uintptr(unsafe.Pointer(destination)), uintptr(unsafe.Pointer(replacement)), uintptr(unsafe.Pointer(backup)), 0, 0, 0)
	return ok != 0, err
}

func commitReplacement(temp string, original *fileSnapshot) error {
	destination, err := windows.UTF16PtrFromString(original.path)
	if err != nil {
		return err
	}
	replacement, err := windows.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	rollback, err := os.CreateTemp(filepath.Dir(original.path), ".lazychezmoi-rollback-*")
	if err != nil {
		return err
	}
	rollbackName := rollback.Name()
	_ = rollback.Close()
	if err := os.Remove(rollbackName); err != nil {
		return err
	}
	keepRollback := false
	defer func() {
		if !keepRollback {
			_ = os.Remove(rollbackName)
		}
	}()
	backup, err := windows.UTF16PtrFromString(rollbackName)
	if err != nil {
		return err
	}
	// The rollback name exists only during this transaction, inherits the
	// original file's security, and is removed afterward. Session undo is
	// memory-only. No IGNORE_ACL/MERGE flags permit metadata loss.
	ok, callErr := callReplaceFile(destination, replacement, backup)
	if ok {
		return nil
	}
	// ReplaceFile documents partial failure states. Inspect the actual paths
	// and never silently retry or restore over an unrelated concurrent edit.
	actual, readErr := captureFile(context.Background(), original.path)
	if readErr == nil && bytes.Equal(actual.data, original.data) && os.SameFile(actual.info, original.info) && equivalentMetadata(actual.metadata, original.metadata) && actual.info.Mode().Perm() == original.info.Mode().Perm() {
		return fmt.Errorf("Windows replacement failed; original content remains: %w", callErr)
	}
	if errors.Is(readErr, os.ErrNotExist) {
		// MoveFile refuses to overwrite a concurrently created destination.
		if err := windows.MoveFile(backup, destination); err == nil {
			return fmt.Errorf("Windows replacement failed; original file restored from transaction scratch: %w", callErr)
		}
		// Only try memory restoration when no OS rollback file exists. A
		// concurrent destination is never overwritten by exclusive creation.
		_, backupErr := os.Lstat(rollbackName)
		if errors.Is(backupErr, os.ErrNotExist) {
			if err := restoreMissingWindowsFile(original); err == nil {
				return fmt.Errorf("Windows replacement failed; original content restored from memory: %w", callErr)
			}
		}
	}
	keepRollback = true
	return &replacementFailure{message: fmt.Sprintf("Windows replacement had a partial or unknown outcome; do not retry. Recovery paths: destination %q, original rollback %q, replacement %q (some may be absent)", original.path, rollbackName, temp), cause: callErr, keepTemp: true}
}

func restoreMissingWindowsFile(original *fileSnapshot) (err error) {
	f, err := os.OpenFile(original.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	sd, err := windows.SecurityDescriptorFromString(original.metadata.security)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	group, _, err := sd.Group()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	flags := windows.SECURITY_INFORMATION(copySecurity)
	if control&windows.SE_DACL_PROTECTED != 0 {
		flags |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		flags |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if err = windows.SetNamedSecurityInfo(original.path, windows.SE_FILE_OBJECT, flags, owner, group, dacl, nil); err != nil {
		return err
	}
	if _, err = f.Write(original.data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	creation := windows.NsecToFiletime(original.metadata.creation)
	if err = windows.SetFileTime(windows.Handle(f.Fd()), &creation, nil, nil); err != nil {
		return err
	}
	ptr, err := windows.UTF16PtrFromString(original.path)
	if err != nil {
		return err
	}
	return windows.SetFileAttributes(ptr, original.metadata.attributes)
}
