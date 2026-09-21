//go:build windows

package chezmoi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

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
		originalSecurity := windowsSecurityShape(t, before.metadata.security)
		currentSecurity := windowsSecurityShape(t, current.metadata.security)
		t.Logf("%s components: owner_equal=%t group_equal=%t dacl_equal=%t before_aces=%v after_aces=%v",
			label, originalSecurity.owner == currentSecurity.owner,
			originalSecurity.group == currentSecurity.group,
			fmt.Sprint(originalSecurity.aces) == fmt.Sprint(currentSecurity.aces),
			originalSecurity.aces, currentSecurity.aces)
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

type securityShape struct {
	owner, group [32]byte
	aces         []string
}

func windowsSecurityShape(t *testing.T, value string) securityShape {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(value)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	group, _, err := sd.Group()
	if err != nil {
		t.Fatal(err)
	}
	shape := securityShape{owner: sha256.Sum256([]byte(owner.String())), group: sha256.Sum256([]byte(group.String()))}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		shape.aces = []string{"null-dacl"}
		return shape
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceSize < 8 {
			t.Fatal("unexpected short ACE")
		}
		body := unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize))
		hash := sha256.Sum256(body)
		sidHash := "not-simple-ace"
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			sum := sha256.Sum256([]byte(sid.String()))
			sidHash = fmt.Sprintf("%x", sum[:8])
		}
		shape.aces = append(shape.aces, fmt.Sprintf("type=%d,flags=%#x,mask=%#x,size=%d,sid_hash=%s,ace_hash=%x", ace.Header.AceType, ace.Header.AceFlags, ace.Mask, ace.Header.AceSize, sidHash, hash[:8]))
	}
	return shape
}

func TestWindowsSecurityEquivalenceOnlyIgnoresAutoInheritedControl(t *testing.T) {
	const original = "O:SYG:BAD:(A;;FA;;;SY)(A;;FR;;;BA)"
	const inherited = "O:SYG:BAD:AI(A;;FA;;;SY)(A;;FR;;;BA)"
	if !equivalentSecurity(original, inherited) || !equivalentSecurity(inherited, original) {
		t.Fatal("automatic-inheritance control bookkeeping changed effective equivalence")
	}
	for name, changed := range map[string]string{
		"owner":      "O:BAG:BAD:AI(A;;FA;;;SY)(A;;FR;;;BA)",
		"group":      "O:SYG:BUD:AI(A;;FA;;;SY)(A;;FR;;;BA)",
		"permission": "O:SYG:BAD:AI(A;;FR;;;SY)(A;;FR;;;BA)",
		"ACE order":  "O:SYG:BAD:AI(A;;FR;;;BA)(A;;FA;;;SY)",
		"ACE flags":  "O:SYG:BAD:AI(A;ID;FA;;;SY)(A;;FR;;;BA)",
		"protection": "O:SYG:BAD:PAI(A;;FA;;;SY)(A;;FR;;;BA)",
		"request":    "O:SYG:BAD:ARAI(A;;FA;;;SY)(A;;FR;;;BA)",
		"invalid":    "not a descriptor",
	} {
		t.Run(name, func(t *testing.T) {
			if equivalentSecurity(original, changed) {
				t.Fatal("a distinct permission/identity/control was accepted")
			}
		})
	}
	a := fileMetadata{security: original, attributes: windows.FILE_ATTRIBUTE_ARCHIVE, creation: 123}
	b := a
	b.security = inherited
	if !equivalentMetadata(a, b) || metadataFingerprint(a) == metadataFingerprint(b) {
		t.Fatal("equivalence must normalize only the comparison, retaining raw snapshot detection")
	}
	b.creation++
	if equivalentMetadata(a, b) {
		t.Fatal("creation time change was ignored")
	}
	b.creation = a.creation
	b.attributes |= windows.FILE_ATTRIBUTE_HIDDEN
	if equivalentMetadata(a, b) {
		t.Fatal("attribute change was ignored")
	}
}
