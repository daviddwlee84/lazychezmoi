package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

type stagedBackend struct {
	fakeBackend
	rows                                      []chezmoi.Entry
	started, release                          chan struct{}
	statusCalls, inventoryCalls, previewCalls atomic.Int32
}

func (f *stagedBackend) Inventory(context.Context, bool) ([]chezmoi.Entry, error) {
	f.inventoryCalls.Add(1)
	return append([]chezmoi.Entry(nil), f.rows...), nil
}
func (f *stagedBackend) Status(ctx context.Context, _ bool) (map[string]chezmoi.EntryStatus, error) {
	if f.statusCalls.Add(1) == 1 {
		close(f.started)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return map[string]chezmoi.EntryStatus{".c": {Pending: "M"}}, nil
	}
}
func (f *stagedBackend) Preview(_ context.Context, e chezmoi.Entry, _ string) (string, error) {
	f.previewCalls.Add(1)
	return "source preview " + e.Relative, nil
}

func splitEffects(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		return msg
	default:
		return []tea.Cmd{func() tea.Msg { return msg }}
	}
}

func TestInventoryAndSourceAreUsableWhileStatusIsHeld(t *testing.T) {
	base, _ := fixture()
	f := &stagedBackend{rows: base.lists[0].entries, started: make(chan struct{}), release: make(chan struct{})}
	m := newModel(context.Background(), f, Options{})
	_, cmd := m.Update(m.loadEntries(0)())
	if !m.lists[0].loaded || !m.lists[0].statusLoading || m.lists[0].statusKnown {
		t.Fatal("inventory did not publish independent readiness")
	}
	if !strings.Contains(m.View().Content, "checking status") {
		t.Fatal("pending status not displayed")
	}
	if m.lists[0].entries[0].Pending != "?" {
		t.Fatal("unobserved status looked clean")
	}
	// Effect order is selected Source preview, then full native status. The
	// status goroutine remains behind an explicit gate until every assertion.
	effects := splitEffects(cmd)
	if len(effects) != 2 {
		t.Fatalf("expected preview/status effects, got %d", len(effects))
	}
	m.Update(effects[0]())
	done := make(chan tea.Msg, 1)
	go func() { done <- effects[1]() }()
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("status did not start")
	}
	if m.lists[0].preview != "source preview .a" {
		t.Fatal("source preview waited on status")
	}
	press(m, "/")
	press(m, ".")
	press(m, "down")
	press(m, "enter")
	entry, _ := m.selectedEntry()
	if entry.ID != "b" {
		t.Fatal("filter/navigation blocked by status")
	}
	for _, a := range m.actions() {
		if a.id == "edit" && !a.enabled {
			t.Fatal("editor blocked by unknown status")
		}
		if a.id == "search" && !a.enabled {
			t.Fatal("search blocked by unknown status")
		}
		if a.id == "changed" && a.enabled {
			t.Fatal("changed-only enabled before a successful status")
		}
	}
	press(m, "s")
	if m.tab != 3 || m.dialog == nil || m.dialog.kind != "search" {
		t.Fatal("content search unavailable while status pending")
	}
	press(m, "esc")
	press(m, "1")
	beforeGen, beforePreview := m.lists[0].previewGen, f.previewCalls.Load()
	m.lists[0].scroll = 7
	close(f.release)
	select {
	case msg := <-done:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("status did not finish")
	}
	entry, _ = m.selectedEntry()
	if entry.ID != "b" || m.lists[0].query != "." || m.lists[0].scroll != 7 {
		t.Fatal("status enrichment lost user context")
	}
	if m.lists[0].previewGen != beforeGen || f.previewCalls.Load() != beforePreview {
		t.Fatal("status completion restarted selected preview")
	}
	if !m.lists[0].statusKnown || !strings.Contains(m.View().Content, "status ready") {
		t.Fatal("completed status not published")
	}
}

func TestStatusSortWaitsForInputAndPreservesPreview(t *testing.T) {
	m, _ := fixture()
	press(m, "/")
	press(m, ".")
	press(m, "down")
	s := &m.lists[0]
	s.statusGen = 5
	s.scroll = 4
	s.checked["b"] = true
	s.preview = "keep this diff"
	before := s.previewGen
	m.Update(statusMsg{tab: 0, gen: 5, inventoryGen: s.gen, statuses: map[string]chezmoi.EntryStatus{".c": {Pending: "M"}}})
	if s.entries[0].ID != "a" || !s.sortPending {
		t.Fatal("status reordered the list during typing")
	}
	if s.entries[2].Pending != "M" {
		t.Fatal("badges were needlessly delayed with sorting")
	}
	press(m, "enter")
	e, _ := m.selectedEntry()
	if s.entries[0].ID != "c" || s.sortPending || e.ID != "b" {
		t.Fatal("deferred changes-first sort failed to preserve selection")
	}
	if s.previewGen != before || s.preview != "keep this diff" || s.scroll != 4 || !s.checked["b"] || s.query != "." {
		t.Fatal("sort restarted or discarded established context")
	}
}

func TestStatusSortWaitsForMouseRelease(t *testing.T) {
	m, _ := featureFixture()
	s := &m.lists[0]
	s.statusGen = 2
	h := hitOf(t, m, "checkbox", "b")
	m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	m.Update(statusMsg{tab: 0, gen: 2, inventoryGen: s.gen, statuses: map[string]chezmoi.EntryStatus{".c": {Pending: "M"}}})
	if s.entries[0].ID != "a" || m.pressed == nil {
		t.Fatal("status completion moved the pressed row")
	}
	m.Update(tea.MouseReleaseMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	if !s.checked["b"] || s.checked["c"] || s.entries[0].ID != "c" {
		t.Fatal("mouse release acted on resorted coordinates")
	}
}

func TestChangedOnlyStatusCannotRetargetPressedApply(t *testing.T) {
	m, _ := featureFixture()
	s := &m.lists[0]
	s.changed = true
	s.statusKnown = true
	s.statusGen = 2
	s.entries[0].Pending = "M"
	s.entries[1].Pending = "M"
	apply := hitOf(t, m, "action", "apply")
	m.Update(tea.MouseClickMsg{X: apply.Rect.X, Y: apply.Rect.Y, Button: tea.MouseLeft})
	m.Update(statusMsg{tab: 0, gen: 2, inventoryGen: s.gen, statuses: map[string]chezmoi.EntryStatus{".b": {Pending: "M"}}})
	e, _ := m.selectedEntry()
	if e.ID != "b" {
		t.Fatal("expected disappeared selection to fall back to remaining changed file")
	}
	m.Update(tea.MouseReleaseMsg{X: apply.Rect.X, Y: apply.Rect.Y, Button: tea.MouseLeft})
	if m.pending != nil {
		t.Fatal("status completion retargeted the pressed Apply action")
	}
}

func TestStatusFailureAndOldRepliesCannotReplaceInventory(t *testing.T) {
	m, _ := fixture()
	s := &m.lists[0]
	s.statusKnown = false
	s.statusGen = 3
	s.gen = 4
	s.statusLoading = true
	for i := range s.entries {
		s.entries[i].Pending = "?"
		s.entries[i].Drift = "?"
	}
	m.Update(statusMsg{tab: 0, gen: 3, inventoryGen: 4, err: errors.New("template failed")})
	if s.stale || s.statusErr == "" || len(s.entries) != 3 || s.statusKnown {
		t.Fatal("status failure damaged valid inventory")
	}
	for _, a := range m.actions() {
		if a.id == "edit" && !a.enabled {
			t.Fatal("status error disabled edit")
		}
	}
	m.loadEntries(0)
	m.Update(statusMsg{tab: 0, gen: 3, inventoryGen: 4, statuses: map[string]chezmoi.EntryStatus{}})
	if s.statusKnown || s.entries[0].Pending != "?" {
		t.Fatal("old status marked a refreshed generation clean")
	}
	current := s.statusGen
	m.invalidate()
	m.Update(statusMsg{tab: 0, gen: current, inventoryGen: s.gen - 1, statuses: map[string]chezmoi.EntryStatus{}})
	if s.statusKnown {
		t.Fatal("pre-mutation status survived invalidation")
	}
}

func TestAutoFetchStartsOnceAfterInventoryResponse(t *testing.T) {
	m, f := fixture()
	m.opts.AutoFetch = true
	m.Init()
	if m.fetching || m.autoFetchStarted || f.calls != 0 {
		t.Fatal("fetch ran before inventory readiness")
	}
	gen := m.lists[0].gen
	m.Update(entriesMsg{tab: 0, gen: gen, entries: m.lists[0].entries})
	if !m.fetching || !m.autoFetchStarted {
		t.Fatal("inventory did not release one background fetch")
	}
	m.fetching = false
	fetchGen := m.fetchGen
	m.Update(entriesMsg{tab: 0, gen: gen, entries: m.lists[0].entries})
	if m.fetching || m.fetchGen != fetchGen {
		t.Fatal("inventory refresh restarted automatic fetch")
	}
}

func TestManualRefreshInvalidatesBackendBeforeStartingReads(t *testing.T) {
	m, f := fixture()
	oldGen := m.lists[0].gen
	cmd := m.refresh()
	if f.calls != 0 || !m.lists[0].stale || m.lists[0].gen == oldGen {
		t.Fatal("refresh blocked Update or left old read generations valid")
	}
	message := cmd()
	if f.calls != 1 {
		t.Fatal("refresh did not invalidate backend cache")
	}
	_, reads := m.Update(message)
	if reads == nil || !m.lists[0].loading {
		t.Fatal("fresh reads failed to start after invalidation")
	}
	if f.calls != 1 {
		t.Fatal("fresh backend reads ran synchronously in Update")
	}
}

func TestReversedRefreshEffectsCannotInvalidateNewestReads(t *testing.T) {
	m, f := fixture()
	old := m.refresh()
	newest := m.refresh()
	m.Update(newest())
	gen, statusGen := m.lists[0].gen, m.lists[0].statusGen
	if f.calls != 1 || !m.lists[0].loading {
		t.Fatal("newest refresh did not start fresh reads")
	}
	if message := old(); message != nil {
		t.Fatal("obsolete refresh effect should be discarded before invalidation")
	}
	if f.calls != 1 || m.lists[0].gen != gen || m.lists[0].statusGen != statusGen {
		t.Fatal("old effect canceled or replaced newest discovery")
	}
}

func TestOldRefreshCannotInvalidatePostMutationReads(t *testing.T) {
	m, f := fixture()
	old := m.refresh()
	prepare := m.beginOperation(&pendingOperation{label: "Edit", op: chezmoi.Operation{Kind: "edit", Targets: []string{"/home/.a"}}})
	message := prepare().(preparedMsg)
	if message.err != nil {
		t.Fatal(message.err)
	}
	_, complete := m.Update(operationMsg{id: m.pending.id})
	m.Update(complete())
	calls, gen := f.calls, m.lists[0].gen
	if m.pending != nil || !m.lists[0].loading {
		t.Fatal("mutation did not finish into fresh discovery")
	}
	if message := old(); message != nil {
		t.Fatal("obsolete refresh survived completed mutation")
	}
	if f.calls != calls || m.lists[0].gen != gen {
		t.Fatal("old refresh invalidated post-mutation reads")
	}
}

type blockingInvalidationBackend struct {
	fakeBackend
	entered, release chan struct{}
	invalidations    atomic.Int32
}

func (f *blockingInvalidationBackend) Invalidate() {
	if f.invalidations.Add(1) == 1 {
		close(f.entered)
		<-f.release
	}
}

func TestWorkerInvalidationGateSerializesAnAlreadyEnteredRefresh(t *testing.T) {
	f := &blockingInvalidationBackend{entered: make(chan struct{}), release: make(chan struct{})}
	m := newModel(t.Context(), f, Options{})
	first := m.refresh()
	doneFirst := make(chan tea.Msg, 1)
	go func() { doneFirst <- first() }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("first worker did not enter invalidation")
	}
	second := m.refresh()
	doneSecond := make(chan tea.Msg, 1)
	go func() { doneSecond <- second() }()
	// Update has already published epoch 2 without waiting for the backend;
	// the second worker must finish its own invalidation after worker 1 exits.
	if m.refreshGen != 2 || f.invalidations.Load() != 1 {
		t.Fatal("Update blocked or workers entered invalidation concurrently")
	}
	close(f.release)
	select {
	case msg := <-doneSecond:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("second worker did not finish")
	}
	select {
	case msg := <-doneFirst:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("first worker did not finish")
	}
	if f.invalidations.Load() != 2 || m.refreshGen != 2 {
		t.Fatal("workers did not serialize under the latest epoch")
	}
}
