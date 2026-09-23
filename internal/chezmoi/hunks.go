package chezmoi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const maxHunkBytes = 2 << 20
const maxHunkLines = 20000
const maxHunkWork = 8000000

var hunkDiffSlots = make(chan struct{}, 2)

type DiffSnapshot struct {
	ID              string   `json:"id"`
	Entry           Entry    `json:"entry"`
	Raw             string   `json:"raw"`
	Hunks           []Hunk   `json:"hunks"`
	Notes           []string `json:"notes"`
	source, current *fileSnapshot
	entry           Entry
	id              string
	hunks           map[string]hunkRange
	guard           *routingGuard
}

type Hunk struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Patch    string `json:"patch"`
	OldStart int    `json:"old_start"`
	OldCount int    `json:"old_count"`
	NewStart int    `json:"new_start"`
	NewCount int    `json:"new_count"`
}

type CopyReceipt struct {
	Path              string `json:"path"`
	before, after     *fileSnapshot
	entry             Entry
	sourceDestination bool
	owner             *Service
	used              bool
	guard             *routingGuard
}

type fileSnapshot struct {
	path     string
	data     []byte
	info     os.FileInfo
	metadata fileMetadata
	id       string
}
type hunkRange struct{ oldStart, oldEnd, newStart, newEnd int }

type replacementFailure struct {
	message  string
	cause    error
	keepTemp bool
}

func (e *replacementFailure) Error() string {
	if e.cause == nil {
		return e.message
	}
	return e.message + ": " + e.cause.Error()
}
func (e *replacementFailure) Unwrap() error { return e.cause }

type controlStamp struct {
	exists    bool
	info      os.FileInfo
	signature string
}
type routingGuard struct {
	files map[string]controlStamp
	dirs  map[string]os.FileInfo
}

func freezeFileIdentity(info os.FileInfo) error {
	// On Windows, Lstat defers the file ID lookup until SameFile is called.
	// Resolve it while capturing the snapshot, before a path can be replaced.
	if !os.SameFile(info, info) {
		return errors.New("cannot capture source-context file identity")
	}
	return nil
}

func readControl(path string) (controlStamp, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return controlStamp{}, nil
	}
	if err != nil {
		return controlStamp{}, err
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return controlStamp{}, errors.New("unsupported source-control file type")
	}
	if err := freezeFileIdentity(info); err != nil {
		return controlStamp{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return controlStamp{}, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxHunkBytes+1))
	if err != nil {
		return controlStamp{}, err
	}
	if len(body) > maxHunkBytes {
		return controlStamp{}, errors.New("source-control file is too large for hunk validation")
	}
	link := ""
	if info.Mode()&os.ModeSymlink != 0 {
		link, err = os.Readlink(path)
		if err != nil {
			return controlStamp{}, err
		}
	}
	return controlStamp{exists: true, info: info, signature: digest(string(body), link, fmt.Sprintf("%d/%d/%d", info.Mode(), info.Size(), info.ModTime().UnixNano()))}, nil
}

func newRoutingGuard(c Context, e Entry) (*routingGuard, error) {
	g := &routingGuard{files: make(map[string]controlStamp), dirs: make(map[string]os.FileInfo)}
	if c.ConfigFile != "" {
		g.files[c.ConfigFile] = controlStamp{}
	}
	for _, root := range []string{c.SourceDir, c.SourceStateDir} {
		if root == "" {
			continue
		}
		for dir := filepath.Dir(e.Source); ; dir = filepath.Dir(dir) {
			rel, err := filepath.Rel(root, dir)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				break
			}
			g.dirs[dir] = nil
			for _, name := range []string{".chezmoiroot", ".chezmoiignore", ".chezmoiignore.tmpl", ".chezmoiremove", ".chezmoiremove.tmpl", ".chezmoidata.json", ".chezmoidata.json.tmpl", ".chezmoidata.toml", ".chezmoidata.toml.tmpl", ".chezmoidata.yaml", ".chezmoidata.yaml.tmpl", ".chezmoidata.yml", ".chezmoidata.yml.tmpl"} {
				g.files[filepath.Join(dir, name)] = controlStamp{}
			}
			if samePath(root, dir) {
				break
			}
		}
	}
	g.dirs[filepath.Dir(e.Target)] = nil
	return g.refresh()
}

func (g *routingGuard) refresh() (*routingGuard, error) {
	fresh := &routingGuard{files: make(map[string]controlStamp, len(g.files)), dirs: make(map[string]os.FileInfo, len(g.dirs))}
	for path := range g.files {
		stamp, err := readControl(path)
		if err != nil {
			return nil, err
		}
		fresh.files[path] = stamp
	}
	for path := range g.dirs {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("source/current parent path is not a plain directory")
		}
		if err := freezeFileIdentity(info); err != nil {
			return nil, err
		}
		fresh.dirs[path] = info
	}
	return fresh, nil
}

func (g *routingGuard) verify(ctx context.Context) error {
	if g == nil {
		return errors.New("missing source-context snapshot")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fresh, err := g.refresh()
	if err != nil {
		return err
	}
	for path, before := range g.files {
		after := fresh.files[path]
		if before.exists != after.exists || before.signature != after.signature || (before.exists && !os.SameFile(before.info, after.info)) {
			return errors.New("chezmoi configuration or source controls changed; refresh before copying")
		}
	}
	for path, before := range g.dirs {
		after := fresh.dirs[path]
		if !os.SameFile(before, after) || before.Mode() != after.Mode() {
			return errors.New("source/current directory mapping changed; refresh before copying")
		}
	}
	return nil
}

func (g *routingGuard) fingerprint() string {
	keys := make([]string, 0, len(g.files))
	for path := range g.files {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	var values []string
	for _, path := range keys {
		v := g.files[path]
		values = append(values, path, strconv.FormatBool(v.exists), v.signature)
	}
	return digest(values...)
}

func eligibleHunkEntry(e Entry) error {
	if e.Kind != "file" || e.Template || e.Encrypted {
		return errors.New("hunk copies require an ordinary, unencrypted, non-template managed file")
	}
	if e.Source == "" || e.Target == "" || samePath(e.Source, e.Target) {
		return errors.New("hunk copies require distinct source and current paths")
	}
	return nil
}

func sourceAllowsEmpty(path string) bool {
	name := filepath.Base(path)
	for _, prefix := range []string{"encrypted_", "private_", "readonly_", "empty_", "executable_", "dot_"} {
		if strings.HasPrefix(name, prefix) {
			if prefix == "empty_" {
				return true
			}
			name = strings.TrimPrefix(name, prefix)
		}
	}
	return false
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func captureFile(ctx context.Context, path string) (*fileSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read existing file: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file; links and special files cannot use hunk copies", path)
	}
	if before.Size() > maxHunkBytes {
		return nil, errors.New("hunk copies are limited to 2 MiB per file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, opened) {
		return nil, errors.New("file changed while opening it; refresh the diff")
	}
	meta, err := captureMetadata(ctx, f, opened)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHunkBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHunkBytes {
		return nil, errors.New("hunk copies are limited to 2 MiB per file")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("hunk copies require UTF-8 text without NUL bytes")
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	final, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !final.Mode().IsRegular() || !os.SameFile(opened, final) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || after.Mode() != opened.Mode() {
		return nil, errors.New("file changed while reading it; refresh the diff")
	}
	metaAfter, err := captureMetadata(ctx, f, after)
	if err != nil {
		return nil, err
	}
	if metadataFingerprint(metaAfter) != metadataFingerprint(meta) {
		return nil, errors.New("file metadata changed while reading it; refresh the diff")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := digest(path, string(data), fmt.Sprintf("%d/%d/%d", opened.Mode(), opened.Size(), opened.ModTime().UnixNano()), metadataFingerprint(meta))
	return &fileSnapshot{path: path, data: data, info: after, metadata: meta, id: id}, nil
}

func splitRawLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func diffGroups(ctx context.Context, old, new []string) ([][]difflib.OpCode, error) {
	prefix := 0
	for prefix < len(old) && prefix < len(new) && old[prefix] == new[prefix] {
		prefix++
	}
	oldEnd, newEnd := len(old), len(new)
	for oldEnd > prefix && newEnd > prefix && old[oldEnd-1] == new[newEnd-1] {
		oldEnd--
		newEnd--
	}
	start := max(0, prefix-3)
	oldEnd = min(len(old), oldEnd+3)
	newEnd = min(len(new), newEnd+3)
	a, b := old[start:oldEnd], new[start:newEnd]
	if len(a) > maxHunkLines || len(b) > maxHunkLines || int64(len(a))*int64(len(b)) > maxHunkWork {
		return nil, errors.New("changed region has too many lines for bounded hunk comparison; use the read-only diff or an editor")
	}
	select {
	case hunkDiffSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	done := make(chan [][]difflib.OpCode, 1)
	go func() {
		defer func() { <-hunkDiffSlots }()
		groups := difflib.NewMatcherWithJunk(a, b, false, nil).GetGroupedOpCodes(3)
		for i := range groups {
			for j := range groups[i] {
				groups[i][j].I1 += start
				groups[i][j].I2 += start
				groups[i][j].J1 += start
				groups[i][j].J2 += start
			}
		}
		done <- groups
	}()
	select {
	case groups := <-done:
		return groups, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func lineRange(start, count int) string {
	if count == 0 {
		return strconv.Itoa(start) + ",0"
	}
	if count == 1 {
		return strconv.Itoa(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}
func patchLine(b *strings.Builder, prefix byte, line string) {
	b.WriteByte(prefix)
	b.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		b.WriteString("\n\\ No newline at end of file\n")
	}
}
func patchBody(group []difflib.OpCode, old, new []string) string {
	var b strings.Builder
	for _, op := range group {
		switch op.Tag {
		case 'e':
			for _, line := range old[op.I1:op.I2] {
				patchLine(&b, ' ', line)
			}
		case 'd':
			for _, line := range old[op.I1:op.I2] {
				patchLine(&b, '-', line)
			}
		case 'i':
			for _, line := range new[op.J1:op.J2] {
				patchLine(&b, '+', line)
			}
		case 'r':
			for _, line := range old[op.I1:op.I2] {
				patchLine(&b, '-', line)
			}
			for _, line := range new[op.J1:op.J2] {
				patchLine(&b, '+', line)
			}
		}
	}
	return b.String()
}

func endingDescription(data []byte) string {
	crlf := bytes.Count(data, []byte("\r\n"))
	lf := bytes.Count(data, []byte("\n")) - crlf
	switch {
	case crlf > 0 && lf > 0:
		return "mixed LF/CRLF"
	case crlf > 0:
		return "CRLF"
	case lf > 0:
		return "LF"
	default:
		return "no line terminators"
	}
}

// Hunks compares exact Current (old) and Source (new) bytes. The public patch
// is a presentation format; writes use private ranges against the snapshots.
func (s *Service) Hunks(ctx context.Context, e Entry) (*DiffSnapshot, error) {
	if err := eligibleHunkEntry(e); err != nil {
		return nil, err
	}
	actual, c, err := s.locateFresh(ctx, e.Source)
	if err != nil {
		return nil, err
	}
	if err := eligibleHunkEntry(actual); err != nil {
		return nil, err
	}
	if actual.ID != e.ID || !samePath(actual.Source, e.Source) {
		return nil, errors.New("managed mapping changed; refresh the diff")
	}
	guard, err := newRoutingGuard(c, actual)
	if err != nil {
		return nil, err
	}
	source, err := captureFile(ctx, actual.Source)
	if err != nil {
		return nil, err
	}
	current, err := captureFile(ctx, actual.Target)
	if err != nil {
		return nil, err
	}
	if os.SameFile(source.info, current.info) {
		return nil, errors.New("source and current refer to the same file")
	}
	if len(bytes.TrimSpace(source.data)) == 0 && !sourceAllowsEmpty(actual.Source) {
		return nil, errors.New("empty source would remove the managed target; use native Apply instead")
	}
	id := digest(actual.ID, actual.Source, actual.Kind, source.id, current.id, guard.fingerprint())
	snapshot := &DiffSnapshot{ID: id, Entry: actual, source: source, current: current, entry: actual, id: id, Hunks: []Hunk{}, Notes: []string{}, hunks: make(map[string]hunkRange), guard: guard}
	if source.info.Mode().Perm() != current.info.Mode().Perm() {
		snapshot.Notes = append(snapshot.Notes, "Source and Current filesystem permissions differ; copies preserve receiving permissions. Apply follows chezmoi filename attributes.")
	}
	if endingDescription(source.data) != endingDescription(current.data) {
		snapshot.Notes = append(snapshot.Notes, "Line endings: Current "+endingDescription(current.data)+" → Source "+endingDescription(source.data))
	}
	if bytes.HasPrefix(source.data, []byte{0xef, 0xbb, 0xbf}) != bytes.HasPrefix(current.data, []byte{0xef, 0xbb, 0xbf}) {
		snapshot.Notes = append(snapshot.Notes, "UTF-8 BOM differs; copied bytes retain the original BOM.")
	}
	if len(source.data) > 0 && !bytes.HasSuffix(source.data, []byte("\n")) {
		snapshot.Notes = append(snapshot.Notes, "Source has no final newline.")
	}
	if len(current.data) > 0 && !bytes.HasSuffix(current.data, []byte("\n")) {
		snapshot.Notes = append(snapshot.Notes, "Current has no final newline.")
	}
	if bytes.Equal(source.data, current.data) {
		return snapshot, nil
	}
	old, new := splitRawLines(current.data), splitRawLines(source.data)
	groups, err := diffGroups(ctx, old, new)
	if err != nil {
		return nil, err
	}
	// Conventional a/b paths allow delta to detect the target language.
	name := filepath.ToSlash(actual.Relative)
	oldName, newName := "a/"+name, "b/"+name
	if strings.ContainsAny(name, "\n\r\t\"\\") {
		oldName = strconv.Quote(oldName)
		newName = strconv.Quote(newName)
	}
	fileHeaders := "--- " + oldName + "\n+++ " + newName + "\n"
	var raw strings.Builder
	raw.WriteString(fileHeaders)
	for _, group := range groups {
		first, last := group[0], group[len(group)-1]
		r := hunkRange{oldStart: first.I1, oldEnd: last.I2, newStart: first.J1, newEnd: last.J2}
		header := fmt.Sprintf("@@ -%s +%s @@", lineRange(r.oldStart, r.oldEnd-r.oldStart), lineRange(r.newStart, r.newEnd-r.newStart))
		body := header + "\n" + patchBody(group, old, new)
		hunkID := digest(id, header, body)
		oldStart, newStart := r.oldStart+1, r.newStart+1
		if r.oldStart == r.oldEnd {
			oldStart = r.oldStart
		}
		if r.newStart == r.newEnd {
			newStart = r.newStart
		}
		snapshot.Hunks = append(snapshot.Hunks, Hunk{ID: hunkID, Header: header, Patch: fileHeaders + body, OldStart: oldStart, OldCount: r.oldEnd - r.oldStart, NewStart: newStart, NewCount: r.newEnd - r.newStart})
		snapshot.hunks[hunkID] = r
		raw.WriteString(body)
	}
	snapshot.Raw = raw.String()
	return snapshot, nil
}

func verifySnapshot(ctx context.Context, expected *fileSnapshot) error {
	if expected == nil {
		return errors.New("missing private snapshot")
	}
	actual, err := captureFile(ctx, expected.path)
	if err != nil {
		return err
	}
	if actual.id != expected.id || !os.SameFile(actual.info, expected.info) {
		return errors.New("file or metadata changed since this diff; refresh before copying")
	}
	return nil
}

func replaceLines(data []byte, start, end int, replacement []string) []byte {
	lines := splitRawLines(data)
	var b strings.Builder
	for _, line := range lines[:start] {
		b.WriteString(line)
	}
	for _, line := range replacement {
		b.WriteString(line)
	}
	for _, line := range lines[end:] {
		b.WriteString(line)
	}
	return []byte(b.String())
}

// CopyHunk copies one reviewed content region with no subprocess. Hunks' earlier
// native inventory discovery follows configured chezmoi discovery hooks; the
// content mutation itself never calls apply, re-add, hooks, or state commands.
func (s *Service) CopyHunk(ctx context.Context, snapshot *DiffSnapshot, hunkID, direction string) (*CopyReceipt, error) {
	s.copyMu.Lock()
	defer s.copyMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.source == nil || snapshot.current == nil || snapshot.id == "" || snapshot.ID != snapshot.id {
		return nil, errors.New("a fresh in-process diff snapshot is required")
	}
	r, ok := snapshot.hunks[hunkID]
	if !ok {
		return nil, errors.New("hunk ID does not belong to this snapshot; refresh before copying")
	}
	if err := snapshot.guard.verify(ctx); err != nil {
		return nil, err
	}
	if err := verifySnapshot(ctx, snapshot.source); err != nil {
		return nil, err
	}
	if err := verifySnapshot(ctx, snapshot.current); err != nil {
		return nil, err
	}
	var receiver *fileSnapshot
	var result []byte
	sourceDestination := false
	switch direction {
	case "source-to-current":
		receiver = snapshot.current
		result = replaceLines(receiver.data, r.oldStart, r.oldEnd, splitRawLines(snapshot.source.data)[r.newStart:r.newEnd])
	case "current-to-source":
		receiver = snapshot.source
		sourceDestination = true
		result = replaceLines(receiver.data, r.newStart, r.newEnd, splitRawLines(snapshot.current.data)[r.oldStart:r.oldEnd])
	default:
		return nil, errors.New("direction must be source-to-current or current-to-source")
	}
	if sourceDestination && len(bytes.TrimSpace(result)) == 0 && !sourceAllowsEmpty(snapshot.entry.Source) {
		return nil, errors.New("copy would make source empty and cause target removal; use an editor or empty_ attribute explicitly")
	}
	if bytes.Equal(result, receiver.data) {
		return nil, errors.New("hunk does not change the receiving file")
	}
	if receiver.info.Mode().Perm()&0222 == 0 {
		return nil, errors.New("receiving file is read-only; hunk copy will not bypass its permissions")
	}
	after, err := s.writeHunkFile(ctx, receiver, result, func() error {
		if err := snapshot.guard.verify(ctx); err != nil {
			return err
		}
		if err := verifySnapshot(ctx, snapshot.source); err != nil {
			return err
		}
		return verifySnapshot(ctx, snapshot.current)
	})
	if err != nil {
		return nil, err
	}
	guard, err := snapshot.guard.refresh()
	if err != nil {
		return nil, fmt.Errorf("copy completed but context verification failed: %w", err)
	}
	return &CopyReceipt{Path: receiver.path, before: receiver, after: after, entry: snapshot.entry, sourceDestination: sourceDestination, owner: s, guard: guard}, nil
}

// UndoCopy is session-only and refuses to replace a file changed after the copy.
func (s *Service) UndoCopy(ctx context.Context, receipt *CopyReceipt) error {
	s.copyMu.Lock()
	defer s.copyMu.Unlock()
	if receipt == nil || receipt.owner != s || receipt.before == nil || receipt.after == nil || receipt.used {
		return errors.New("no unused copy receipt is available in this session")
	}
	if err := receipt.guard.verify(ctx); err != nil {
		return err
	}
	if err := verifySnapshot(ctx, receipt.after); err != nil {
		return fmt.Errorf("cannot undo: %w", err)
	}
	_, err := s.writeHunkFile(ctx, receipt.after, receipt.before.data, func() error {
		if err := receipt.guard.verify(ctx); err != nil {
			return err
		}
		return verifySnapshot(ctx, receipt.after)
	})
	if err == nil {
		receipt.used = true
	}
	return err
}

func (s *Service) writeHunkFile(ctx context.Context, receiver *fileSnapshot, data []byte, recheck func() error) (*fileSnapshot, error) {
	if len(data) > maxHunkBytes {
		return nil, errors.New("copy result exceeds 2 MiB")
	}
	// Check actual write access without truncating, including ACLs. Replacing
	// via a writable parent directory must not bypass a receiving file's ACL.
	check, err := os.OpenFile(receiver.path, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("receiving file does not allow writing: %w", err)
	}
	info, statErr := check.Stat()
	_ = check.Close()
	if statErr != nil {
		return nil, statErr
	}
	if !os.SameFile(info, receiver.info) {
		return nil, errors.New("receiving file changed before copy preparation")
	}
	temp, err := prepareReplacement(ctx, receiver, data)
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temp)
		}
	}()
	if err := recheck(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := commitReplacement(temp, receiver); err != nil {
		var partial *replacementFailure
		if errors.As(err, &partial) && partial.keepTemp {
			cleanup = false
		}
		return nil, err
	}
	// A committed operation is verified even if its request was just canceled.
	after, err := captureFile(context.Background(), receiver.path)
	if err != nil {
		return nil, fmt.Errorf("copy may have completed but verification failed: %w", err)
	}
	if !bytes.Equal(after.data, data) || !equivalentMetadata(receiver.metadata, after.metadata) || receiver.info.Mode().Perm() != after.info.Mode().Perm() {
		return nil, errors.New("copy completed but file contents or metadata changed during verification; refresh before further actions")
	}
	return after, nil
}
