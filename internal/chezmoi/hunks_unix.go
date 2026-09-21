//go:build darwin || linux

package chezmoi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type fileMetadata struct {
	identity, changed string
	uid, gid          int
	links             uint64
	flags             uint32
	xattrs            map[string][]byte
	security          []byte
}

func captureMetadata(ctx context.Context, f *os.File, info os.FileInfo) (fileMetadata, error) {
	m, err := statMetadata(f)
	if err != nil {
		return m, err
	}
	if m.links != 1 {
		return m, errors.New("hardlinked files cannot use hunk copies")
	}
	if m.uid != os.Geteuid() {
		return m, errors.New("hunk copies require files owned by the current user")
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return m, errors.New("special permission bits are unsupported for hunk copies")
	}
	if err := ctx.Err(); err != nil {
		return m, err
	}
	m.xattrs, err = readXattrs(f)
	return m, err
}

func readXattrs(f *os.File) (map[string][]byte, error) {
	result := make(map[string][]byte)
	size, err := unix.Flistxattr(int(f.Fd()), nil)
	if errors.Is(err, unix.ENOTSUP) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot inspect extended attributes safely: %w", err)
	}
	if size > 1<<20 {
		return nil, errors.New("extended attribute metadata is too large to preserve safely")
	}
	names := make([]byte, size)
	if size > 0 {
		size, err = unix.Flistxattr(int(f.Fd()), names)
		if err != nil {
			return nil, err
		}
		names = names[:size]
	}
	total := 0
	for _, name := range strings.Split(string(names), "\x00") {
		if name == "" {
			continue
		}
		n, err := unix.Fgetxattr(int(f.Fd()), name, nil)
		if err != nil {
			return nil, err
		}
		total += n
		if total > 1<<20 {
			return nil, errors.New("extended attribute metadata is too large to preserve safely")
		}
		value := make([]byte, n)
		if n > 0 {
			n, err = unix.Fgetxattr(int(f.Fd()), name, value)
			if err != nil {
				return nil, err
			}
			value = value[:n]
		}
		result[name] = value
	}
	return result, nil
}

func xattrSignature(attrs map[string][]byte) string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var values []string
	for _, k := range keys {
		values = append(values, k, string(attrs[k]))
	}
	return digest(values...)
}
func metadataFingerprint(m fileMetadata) string {
	return digest(m.identity, m.changed, fmt.Sprintf("%d/%d/%d/%d", m.uid, m.gid, m.links, m.flags), xattrSignature(m.xattrs), string(m.security))
}
func equivalentMetadata(a, b fileMetadata) bool {
	return a.uid == b.uid && a.gid == b.gid && a.flags == b.flags && xattrSignature(a.xattrs) == xattrSignature(b.xattrs) && bytes.Equal(a.security, b.security)
}

func populateReplacement(ctx context.Context, f *os.File, original *fileSnapshot, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.Chown(original.metadata.uid, original.metadata.gid); err != nil {
		return fmt.Errorf("cannot preserve file ownership: %w", err)
	}
	if err := f.Chmod(original.info.Mode().Perm()); err != nil {
		return err
	}
	// Install the original access policy while scratch is still empty. This
	// avoids exposing content through an inherited ACL while changing mode.
	if err := preserveSecurity(f, original.metadata); err != nil {
		return err
	}
	if err := preserveXattrs(f, original.metadata.xattrs); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	meta, err := captureMetadata(ctx, f, info)
	if err != nil {
		return err
	}
	if !equivalentMetadata(original.metadata, meta) || info.Mode().Perm() != original.info.Mode().Perm() {
		return errors.New("temporary replacement cannot preserve original metadata; no copy performed")
	}
	return nil
}

func preserveXattrs(f *os.File, want map[string][]byte) error {
	have, err := readXattrs(f)
	if err != nil {
		return err
	}
	for name := range have {
		if _, ok := want[name]; !ok {
			if err := unix.Fremovexattr(int(f.Fd()), name); err != nil {
				return fmt.Errorf("cannot preserve extended attributes: %w", err)
			}
		}
	}
	for name, value := range want {
		if current, ok := have[name]; ok && bytes.Equal(current, value) {
			continue
		}
		if err := unix.Fsetxattr(int(f.Fd()), name, value, 0); err != nil {
			return fmt.Errorf("cannot preserve extended attribute %s: %w", name, err)
		}
	}
	return nil
}

func commitReplacement(temp string, original *fileSnapshot) error {
	if err := os.Rename(temp, original.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(original.path))
	if err != nil {
		return fmt.Errorf("copy completed but directory sync failed: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("copy completed but directory sync failed: %w", err)
	}
	return nil
}
