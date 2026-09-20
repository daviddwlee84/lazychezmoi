package tui

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

type fakeBackend struct {
	calls        int
	operations   []chezmoi.Operation
	resetEntry   chezmoi.Entry
	resetRecords []chezmoi.ScriptRecord
}

func (f *fakeBackend) Invalidate() { f.calls++ }

func (f *fakeBackend) Resolve(context.Context) (chezmoi.Context, error) {
	f.calls++
	return chezmoi.Context{}, nil
}
func (f *fakeBackend) Entries(context.Context, bool) ([]chezmoi.Entry, error) {
	f.calls++
	return nil, nil
}
func (f *fakeBackend) GitStatus(context.Context) (chezmoi.GitStatus, error) {
	f.calls++
	return chezmoi.GitStatus{}, nil
}
func (f *fakeBackend) Fetch(context.Context, bool) error { f.calls++; return nil }
func (f *fakeBackend) Preview(context.Context, chezmoi.Entry, string) (string, error) {
	f.calls++
	return "preview", nil
}
func (f *fakeBackend) Command(_ context.Context, op chezmoi.Operation) (*exec.Cmd, error) {
	f.calls++
	f.operations = append(f.operations, op)
	return exec.Command("unused-in-test"), nil
}
func (f *fakeBackend) ScriptRecords(context.Context, chezmoi.Entry) ([]chezmoi.ScriptRecord, error) {
	f.calls++
	return nil, nil
}
func (f *fakeBackend) ResetScript(_ context.Context, e chezmoi.Entry, r []chezmoi.ScriptRecord) error {
	f.calls++
	f.resetEntry = e
	f.resetRecords = r
	return nil
}
func (f *fakeBackend) ScriptResult(context.Context, chezmoi.Entry, time.Time) (string, error) {
	f.calls++
	return "ran", nil
}

func fixture() (*model, *fakeBackend) {
	f := &fakeBackend{}
	m := newModel(context.Background(), f, Options{})
	m.lists[0].entries = []chezmoi.Entry{{ID: "a", Target: "/home/.a", Source: "/src/dot_a", Relative: ".a", Kind: "file"}, {ID: "b", Target: "/home/.b", Source: "/src/dot_b", Relative: ".b", Kind: "file"}, {ID: "c", Target: "/home/.c", Source: "/src/dot_c", Relative: ".c", Kind: "file"}}
	m.lists[0].loaded = true
	return m, f
}

func key(k string) tea.KeyPressMsg {
	codes := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "tab": tea.KeyTab, "home": tea.KeyHome, "end": tea.KeyEnd, "backspace": tea.KeyBackspace, "space": tea.KeySpace}
	if code, ok := codes[k]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

func press(m *model, k string) tea.Cmd { _, cmd := m.Update(key(k)); return cmd }

func TestStartupAndViewNeverCallService(t *testing.T) {
	f := &fakeBackend{}
	m := newModel(context.Background(), f, Options{AutoFetch: true})
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected startup effects")
	}
	m.View()
	press(m, "?")
	m.View()
	press(m, "esc")
	press(m, "2")
	m.View()
	if f.calls != 0 {
		t.Fatalf("constructor, Update or View ran %d I/O calls", f.calls)
	}
}

func TestFilterOwnsPrintableAndPastedKeys(t *testing.T) {
	m, f := fixture()
	press(m, "/")
	for _, k := range []string{"j", "k", "q", "h", "l", "/", "?", "e", "a", "u"} {
		press(m, k)
	}
	if got := m.lists[0].query; got != "jkqhl/?eau" {
		t.Fatalf("query %q", got)
	}
	if m.dialog == nil || m.dialog.kind != "filter" || m.pending != nil || f.calls != 0 {
		t.Fatal("typing escaped input ownership")
	}
	m.Update(tea.PasteMsg{Content: "\nq\renter\n"})
	if m.dialog == nil || m.pending != nil {
		t.Fatal("paste submitted or closed filter")
	}
	if strings.ContainsAny(m.lists[0].query, "\r\n") {
		t.Fatal("paste inserted line breaks")
	}
	press(m, "enter")
	if m.dialog != nil || m.detail || m.pending != nil {
		t.Fatal("Enter must only accept query")
	}
	press(m, "/")
	press(m, "esc")
	if m.lists[0].query != "" {
		t.Fatal("Esc should clear query")
	}
}

func TestFilterSelectionCursorAndNavigation(t *testing.T) {
	m, _ := fixture()
	press(m, "/")
	press(m, ".")
	press(m, "down")
	if m.lists[0].selected != 1 {
		t.Fatal("down should select while filtering")
	}
	press(m, "left")
	if m.lists[0].selected != 1 {
		t.Fatal("cursor movement reset selection")
	}
	press(m, "enter")
	press(m, "j")
	if m.lists[0].selected != 2 {
		t.Fatal("j selection differs from arrows")
	}
	press(m, "k")
	if m.lists[0].selected != 1 {
		t.Fatal("k should select previous")
	}
	press(m, "g")
	press(m, "g")
	if m.lists[0].selected != 0 || m.prefix {
		t.Fatal("gg failed")
	}
	press(m, "G")
	if m.lists[0].selected != 2 {
		t.Fatal("G failed")
	}
}

func TestPaletteTypingAndContextualRegistry(t *testing.T) {
	m, _ := fixture()
	press(m, ":")
	for _, k := range []string{"e", "d", "i", "t"} {
		press(m, k)
	}
	if m.pending != nil {
		t.Fatal("palette letters executed an action")
	}
	items := m.paletteActions()
	if len(items) == 0 {
		t.Fatal("missing edit action")
	}
	press(m, "esc")
	if m.lists[0].selected != 0 {
		t.Fatal("palette changed selection")
	}
	for _, a := range m.actions() {
		if a.key != "" && a.enabled && a.id != "next-preview" {
			if !strings.Contains(m.dialogHelp(), a.label) {
				t.Fatalf("action missing from help: %s", a.id)
			}
		}
	}
}

func (m *model) dialogHelp() string {
	old := m.dialog
	m.dialog = &dialog{kind: "help"}
	view := strings.Join(m.dialogView(140, 100), "\n")
	m.dialog = old
	return view
}

func TestRefreshFollowsIdentityAndRejectsOldReplies(t *testing.T) {
	m, _ := fixture()
	m.lists[0].selected = 1
	m.lists[0].gen = 3
	entries := []chezmoi.Entry{{ID: "b", Relative: ".z", Target: "/home/.b"}, {ID: "a", Relative: ".a", Target: "/home/.a", Pending: "M"}}
	m.Update(entriesMsg{tab: 0, gen: 3, entries: entries})
	selected, _ := m.selectedEntry()
	if selected.ID != "b" {
		t.Fatalf("refresh lost selected identity: %q", selected.ID)
	}
	m.Update(entriesMsg{tab: 0, gen: 2, err: errors.New("late failure")})
	if m.lists[0].err != "" {
		t.Fatal("old failure overwrote current snapshot")
	}
	m.Update(entriesMsg{tab: 0, gen: 3, entries: nil})
	if len(m.lists[0].entries) != 0 || m.lists[0].selected != 0 {
		t.Fatal("successful empty response must clear obsolete rows")
	}
}

func TestFailedRefreshRetainsRowsAndPartialInventory(t *testing.T) {
	m, _ := fixture()
	m.lists[0].gen = 1
	m.Update(entriesMsg{tab: 0, gen: 1, err: errors.New("offline")})
	if len(m.lists[0].entries) != 3 || !m.lists[0].stale || m.lists[0].err == "" {
		t.Fatal("failed refresh discarded useful state")
	}
	m.Update(entriesMsg{tab: 0, gen: 1, entries: []chezmoi.Entry{{ID: "partial", Target: "/home/x", Relative: "x", Drift: "?", Pending: "?"}}, err: errors.New("status render failed")})
	if len(m.lists[0].entries) != 1 || !m.lists[0].stale || m.lists[0].err == "" {
		t.Fatal("partial inventory must remain available with error")
	}
}

func TestPreviewAndDialogResultsCannotResurrectOldSelection(t *testing.T) {
	m, _ := fixture()
	m.loadPreview()
	old := m.lists[0].previewGen
	press(m, "j")
	m.Update(previewMsg{tab: 0, gen: old, id: "a", view: "source", content: "OLD"})
	if m.lists[0].preview == "OLD" {
		t.Fatal("late preview applied")
	}
	m.dialog = &dialog{kind: "records", gen: 4, loading: true}
	press(m, "esc")
	m.Update(recordsMsg{gen: 4, records: []chezmoi.ScriptRecord{{Bucket: "scriptState", Key: "hash"}}})
	if m.dialog != nil {
		t.Fatal("late records reopened dialog")
	}
}

func TestMutationWaitsForFetchAndInvalidatesSnapshots(t *testing.T) {
	m, f := fixture()
	m.fetching = true
	m.fetchGen = 5
	cancelled := false
	m.fetchCancel = func() { cancelled = true }
	old := m.lists[0].gen
	cmd := press(m, "a")
	if cmd != nil || !cancelled || m.pending == nil || f.calls != 0 {
		t.Fatal("mutation did not queue behind fetch")
	}
	if m.lists[0].gen <= old || !m.lists[0].stale {
		t.Fatal("write did not invalidate snapshot")
	}
	id := m.pending.id
	press(m, "a")
	if m.pending.id != id {
		t.Fatal("repeat apply queued another mutation")
	}
	_, cmd = m.Update(fetchedMsg{gen: 5, err: context.Canceled})
	if cmd == nil {
		t.Fatal("fetch completion failed to release queued operation")
	}
	msg := cmd()
	prepared, ok := msg.(preparedMsg)
	if !ok || prepared.err != nil {
		t.Fatalf("prepare failed: %#v", msg)
	}
	if len(f.operations) != 1 || !f.operations[0].ExcludeScripts || !reflect.DeepEqual(f.operations[0].Targets, []string{"/home/.a"}) {
		t.Fatalf("wrong scoped apply: %#v", f.operations)
	}
	m.Update(entriesMsg{tab: 0, gen: old, entries: []chezmoi.Entry{{ID: "obsolete"}}})
	if len(m.lists[0].entries) != 3 {
		t.Fatal("pre-write read replaced stale usable snapshot")
	}
}

func TestApplyMarkedFilesIncludesFilteredSelections(t *testing.T) {
	m, _ := fixture()
	press(m, "space")
	press(m, "j")
	press(m, "space")
	m.lists[0].query = ".c"
	m.lists[0].selected = 0
	press(m, "a")
	if !reflect.DeepEqual(m.pending.op.Targets, []string{"/home/.a", "/home/.b"}) {
		t.Fatalf("marked files lost: %#v", m.pending.op.Targets)
	}
}

func TestScriptResetRequiresExactReviewAndReportsPartialFailure(t *testing.T) {
	m, f := fixture()
	e := chezmoi.Entry{ID: "s", Source: "/src/run_once_setup.sh", Kind: "script-once"}
	records := []chezmoi.ScriptRecord{{Bucket: "scriptState", Key: "exact-content-hash"}}
	m.dialog = &dialog{kind: "records", entry: e, records: records, checked: map[int]bool{}, applyAfterReset: true}
	press(m, "enter")
	if m.dialog.kind != "records" || m.pending != nil {
		t.Fatal("unselected state record approved")
	}
	press(m, "space")
	press(m, "enter")
	if m.dialog.kind != "confirm" || m.pending != nil {
		t.Fatal("record choice skipped review")
	}
	if !strings.Contains(strings.Join(m.dialogView(100, 20), "\n"), "exact-content-hash") {
		t.Fatal("review omits exact state key")
	}
	cmd := press(m, "enter")
	msg := cmd()
	if !reflect.DeepEqual(f.resetRecords, records) || f.resetEntry.Source != e.Source {
		t.Fatal("wrong script records reset")
	}
	_, cmd = m.Update(msg)
	prepared := cmd().(preparedMsg)
	if prepared.err != nil || m.pending.resetDone != true || f.operations[0].Targets[0] != e.Source {
		t.Fatal("reset and apply chain incorrect")
	}
	_, cmd = m.Update(operationMsg{id: m.pending.id, err: errors.New("exit 1")})
	m.Update(cmd())
	if !strings.Contains(m.status, "State reset succeeded") || !m.statusErr {
		t.Fatal("partial failure was hidden")
	}
}

func TestReaddOnlyForPlainFilesAndRequiresReview(t *testing.T) {
	for _, e := range []chezmoi.Entry{{Kind: "create"}, {Kind: "modify"}, {Kind: "file", Template: true}, {Kind: "file", Encrypted: true}} {
		m, _ := fixture()
		m.lists[0].entries = []chezmoi.Entry{e}
		m.dispatch("re-add")
		if m.dialog != nil || m.pending != nil {
			t.Fatal("unsafe re-add was offered")
		}
	}
	m, _ := fixture()
	m.dispatch("re-add")
	if m.dialog == nil || m.dialog.kind != "confirm" || m.pending != nil {
		t.Fatal("plain file re-add needs exact-target review")
	}
}

func TestLayoutClampsCellsAndPreservesGraphemes(t *testing.T) {
	m, _ := fixture()
	m.lists[0].entries[0].Relative = "專案 é 👩🏽‍💻 config.toml"
	m.lists[0].preview = "wide 專案 é 👩🏽‍💻"
	for _, size := range [][2]int{{120, 40}, {80, 24}, {50, 12}, {8, 4}, {1, 1}, {0, 0}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, detail := range []bool{false, true} {
			m.detail = detail
			v := m.View()
			lines := strings.Split(v.Content, "\n")
			if len(lines) > max(1, size[1]) {
				t.Fatalf("height overflow %v", size)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > max(1, size[0]) {
					t.Fatalf("width overflow %v: %q", size, line)
				}
			}
		}
	}
	m.width = 100
	m.height = 24
	m.detail = false
	if !strings.Contains(m.View().Content, "é 👩🏽‍💻") {
		t.Fatal("view broke grapheme-bearing filename")
	}
	if fit("👩🏽‍💻X", 2) != "👩🏽‍💻" {
		t.Fatalf("truncation split grapheme: %q", fit("👩🏽‍💻X", 2))
	}
}

func TestNoColorAndUntrustedControlSequences(t *testing.T) {
	m, _ := fixture()
	m.lists[0].entries[0].Relative = "safe\x1b]52;c;c2VjcmV0\a\x1b[2Jname\x07"
	press(m, "/")
	v := m.View().Content
	if strings.Contains(v, "\x1b") || strings.Contains(v, "\x07") {
		t.Fatalf("unexpected ANSI/control in no-color view: %q", v)
	}
	if got := sanitize("a\x1b]0;evil\ab\x1b[Hc\r\x00\t\n"); got != "abc\t\n" {
		t.Fatalf("control sanitizer: %q", got)
	}
}

func TestReloadReplyCannotQuitAnUnrelatedMutation(t *testing.T) {
	m, _ := fixture()
	gen := m.operationGen
	press(m, "a")
	m.Update(reloadCheckedMsg{gen: gen})
	if m.reload {
		t.Fatal("late reload validation interrupted a new operation")
	}
}

func TestHeaderKeepsGitAndFetchVisibleWithLongPaths(t *testing.T) {
	m, _ := fixture()
	m.width = 80
	m.scope.SourceDir = "/a/very/long/path/with/many/components/and/a/source/tree/named/chezmoi"
	m.gitLoaded = true
	m.git = chezmoi.GitStatus{Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 3, Dirty: true, FetchedAt: time.Date(2026, 9, 21, 9, 40, 0, 0, time.UTC)}
	header := m.header()
	for _, part := range []string{"main", "↑2 ↓3", "fetched:", "chezmoi"} {
		if !strings.Contains(header, part) {
			t.Fatalf("header lost %q: %q", part, header)
		}
	}
	if ansi.StringWidth(header) > 80 {
		t.Fatalf("header overflow: %q", header)
	}
	m.fetching = true
	if !strings.Contains(m.header(), "fetching…") {
		t.Fatal("fetching hidden by long path")
	}
	if !strings.Contains(m.footer(), "q quit") {
		t.Fatal("quit hint clipped at 80 columns")
	}
	m.dispatch("context")
	contextView := strings.Join(m.dialogView(100, 30), "\n")
	if !strings.Contains(contextView, m.scope.SourceDir) {
		t.Fatal("full source not inspectable")
	}
}

func TestMultilineMetadataCannotBreakScreenBounds(t *testing.T) {
	m, _ := fixture()
	m.status = "command failed\nstderr line 2"
	m.lists[0].entries[0].Relative = "odd\nfile\tname"
	m.scope.SourceDir = "odd\nsource"
	v := m.View().Content
	if lines := strings.Count(v, "\n") + 1; lines != m.height {
		t.Fatalf("metadata injected rows: got %d", lines)
	}
}

func TestUpdateDefaultsApplyAndAllowsSkippingInit(t *testing.T) {
	for _, id := range []string{"update", "update-no-init"} {
		m, _ := fixture()
		m.dispatch(id)
		if m.pending == nil || m.pending.op.Kind != "update" || !m.pending.op.Apply {
			t.Fatalf("%s failed to request apply", id)
		}
		if m.pending.op.Init != (id == "update") {
			t.Fatalf("%s has wrong init preference", id)
		}
	}
}

func TestScriptEditDoesNotExcludeSelectedScript(t *testing.T) {
	m, _ := fixture()
	m.tab = 1
	m.lists[1].entries = []chezmoi.Entry{{ID: "script", Source: "/source/run_once_setup.sh", Target: "/home/setup.sh", Kind: "script-once"}}
	m.dispatch("edit")
	if m.pending == nil || m.pending.op.ExcludeScripts || len(m.pending.op.Targets) != 1 || m.pending.op.Targets[0] != "/source/run_once_setup.sh" {
		t.Fatal("script editor excluded or retargeted selected script")
	}
}

func TestCompletionInvalidatesBackendAsEffectBeforeRefresh(t *testing.T) {
	m, f := fixture()
	m.dispatch("edit")
	id := m.pending.id
	_, cmd := m.Update(operationMsg{id: id})
	if f.calls != 0 || m.pending == nil {
		t.Fatal("completion performed I/O in Update or released operation early")
	}
	message := cmd()
	if f.calls != 1 {
		t.Fatal("completion did not invalidate service context")
	}
	m.Update(message)
	if m.pending != nil || m.lists[0].view != 3 || !m.lists[0].loading {
		t.Fatal("edit completion failed to return to refreshed Diff")
	}
}

func TestRegistryHasNoActiveShortcutConflicts(t *testing.T) {
	m, _ := fixture()
	m.lists[1].entries = []chezmoi.Entry{{ID: "s", Kind: "script"}}
	for tab := 0; tab < 3; tab++ {
		m.tab = tab
		seen := map[string]string{}
		for _, a := range m.actions() {
			if !a.enabled || a.key == "" {
				continue
			}
			if previous := seen[a.key]; previous != "" {
				t.Fatalf("tab %d: %s and %s share %s", tab, previous, a.id, a.key)
			}
			seen[a.key] = a.id
		}
	}
}

func TestUnknownStatusIsNotClaimedAsKnownChange(t *testing.T) {
	m, _ := fixture()
	m.lists[0].entries[0].Drift = "?"
	m.lists[0].entries[0].Pending = "?"
	if entryChanged(m.lists[0].entries[0]) {
		t.Fatal("unknown status claimed as changed")
	}
	view := strings.Join(m.detailPane(100, 20), "\n")
	if !strings.Contains(view, "local: unknown") || !strings.Contains(view, "apply: unknown") {
		t.Fatal("unknown state omitted from preview")
	}
}
