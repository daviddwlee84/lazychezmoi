package tui

import (
	"context"
	"errors"
	"image/color"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
	"github.com/daviddwlee84/lazychezmoi/internal/diffview"
	"github.com/daviddwlee84/lazychezmoi/internal/search"
)

type featureBackend struct {
	fakeBackend
	snapshot                 *chezmoi.DiffSnapshot
	direction, path          string
	copies, undos, hunkReads int
}

func (f *featureBackend) Hunks(context.Context, chezmoi.Entry) (*chezmoi.DiffSnapshot, error) {
	f.hunkReads++
	return f.snapshot, nil
}
func (f *featureBackend) CopyHunk(_ context.Context, s *chezmoi.DiffSnapshot, id, direction string) (*chezmoi.CopyReceipt, error) {
	f.copies++
	f.direction = direction
	return &chezmoi.CopyReceipt{Path: s.Entry.Target}, nil
}
func (f *featureBackend) UndoCopy(context.Context, *chezmoi.CopyReceipt) error { f.undos++; return nil }
func (f *featureBackend) EditSearchFile(_ context.Context, path string) (*exec.Cmd, error) {
	f.path = path
	return exec.Command("unused-editor"), nil
}

type fakeSearch struct {
	calls   int
	queries []search.Query
	result  search.Result
}

func (f *fakeSearch) Search(_ context.Context, _ *chezmoi.Service, q search.Query) (search.Result, error) {
	f.calls++
	f.queries = append(f.queries, q)
	return f.result, nil
}
func (f *fakeSearch) Preview(context.Context, search.Match) (string, error) {
	return "first\nsecond answer\nlast\n", nil
}

func featureFixture() (*model, *featureBackend) {
	base, _ := fixture()
	f := &featureBackend{}
	m := newModel(context.Background(), f, Options{Mouse: true})
	m.lists[0] = base.lists[0]
	f.snapshot = &chezmoi.DiffSnapshot{ID: "snapshot", Entry: m.lists[0].entries[0], Raw: "raw-full", Hunks: []chezmoi.Hunk{{ID: "one", Header: "@@ -1 +1 @@", Patch: "-old\n+new"}, {ID: "two", Header: "@@ -9 +9 @@", Patch: "-old2\n+new2"}}}
	m.renderer = func(_ context.Context, raw string, opts diffview.Options) (diffview.Result, error) {
		return diffview.Result{Text: raw, Renderer: "fake"}, nil
	}
	m.searcher = &fakeSearch{}
	return m, f
}

func hitOf(t *testing.T, m *model, kind, id string) *hitTarget {
	t.Helper()
	for _, h := range m.layout().Hits {
		if h.Kind == kind && (id == "" || h.ID == id) {
			copy := h
			return &copy
		}
	}
	t.Fatalf("missing hit %s %s", kind, id)
	return nil
}
func click(m *model, h *hitTarget) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	return cmd
}

func TestMouseUsesRenderedRowsAndCheckboxes(t *testing.T) {
	m, _ := featureFixture()
	m.lists[0].query = "."
	m.lists[0].err = "retained snapshot"
	m.lists[0].checked["c"] = true
	h := hitOf(t, m, "row", "b")
	rendered := strings.Split(m.View().Content, "\n")[h.Rect.Y]
	if !strings.Contains(ansi.Strip(rendered), ".b") {
		t.Fatalf("hit map disagrees with row rendering: %s", rendered)
	}
	click(m, h)
	if m.lists[0].selected != 1 || m.detail {
		t.Fatal("row click failed to select and focus")
	}
	click(m, hitOf(t, m, "checkbox", "b"))
	if !m.lists[0].checked["b"] {
		t.Fatal("checkbox did not toggle")
	}
	m.detail = true
	list := m.layout().List
	m.Update(tea.MouseWheelMsg{X: list.X + 3, Y: list.Y + 2, Button: tea.MouseWheelDown})
	if m.lists[0].selected != 2 || !m.detail {
		t.Fatal("wheel failed to target hovered pane without stealing focus")
	}
}

func TestMousePressCancelledByResizeRefreshAndDrag(t *testing.T) {
	for _, change := range []string{"resize", "refresh", "drag", "key"} {
		m, _ := featureFixture()
		h := hitOf(t, m, "checkbox", "a")
		m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
		switch change {
		case "resize":
			m.Update(tea.WindowSizeMsg{Width: 110, Height: 28})
		case "refresh":
			m.Update(entriesMsg{tab: 0, gen: m.lists[0].gen, entries: m.lists[0].entries})
		case "drag":
			m.Update(tea.MouseMotionMsg{X: 99, Y: 0, Button: tea.MouseLeft})
		case "key":
			press(m, "?")
		}
		m.Update(tea.MouseReleaseMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
		if m.lists[0].checked["a"] {
			t.Fatalf("%s allowed stale press", change)
		}
	}
}

func TestMouseModalOwnsClicksAndRequiresExplicitRun(t *testing.T) {
	m, _ := featureFixture()
	old := hitOf(t, m, "checkbox", "a")
	m.openInput("palette")
	click(m, old)
	if m.lists[0].checked["a"] || m.pending != nil {
		t.Fatal("modal clicked through")
	}
	row := hitOf(t, m, "palette-row", "edit")
	click(m, row)
	if m.pending != nil {
		t.Fatal("row click executed action")
	}
	click(m, hitOf(t, m, "dialog", "run"))
	if m.pending == nil || m.pending.op.Kind != "edit" {
		t.Fatal("explicit Run failed")
	}
}

func TestMouseToggleAndX10Release(t *testing.T) {
	m, _ := featureFixture()
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse capture not enabled")
	}
	press(m, "m")
	if m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("mouse capture did not disable")
	}
	h := hitOf(t, m, "checkbox", "a")
	click(m, h)
	if m.lists[0].checked["a"] {
		t.Fatal("disabled mouse processed click")
	}
	press(m, "m")
	m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseNone})
	if !m.lists[0].checked["a"] {
		t.Fatal("X10 release was ignored")
	}
}

func TestRightClickActionsTargetHoveredRow(t *testing.T) {
	m, _ := featureFixture()
	row := hitOf(t, m, "row", "b")
	m.Update(tea.MouseClickMsg{X: row.Rect.X, Y: row.Rect.Y, Button: tea.MouseRight})
	if m.dialog == nil || m.dialog.kind != "palette" || m.lists[0].selected != 1 {
		t.Fatal("right click did not select row before opening actions")
	}
	click(m, hitOf(t, m, "palette-row", "edit"))
	click(m, hitOf(t, m, "dialog", "run"))
	if m.pending == nil || m.pending.op.Targets[0] != "/home/.b" {
		t.Fatal("context action targeted previous selection")
	}
}

func TestMouseHunkButtonsMatchRenderingAndCopyDirection(t *testing.T) {
	m, f := featureFixture()
	s := &m.lists[0]
	s.view = 3
	s.snapshot = f.snapshot
	s.hunkMode = true
	s.previewID = "a"
	s.renderLayout = "side-by-side"
	s.renderName = "delta"
	m.width = 180
	for _, id := range []string{"prev-hunk", "next-hunk", "hunk-to-current", "hunk-to-source"} {
		h := hitOf(t, m, "hunk-action", id)
		line := ansi.Strip(strings.Split(m.View().Content, "\n")[h.Rect.Y])
		if !strings.Contains(line, map[string]string{"prev-hunk": "[Prev]", "next-hunk": "[Next]", "hunk-to-current": "[< Source → Current]", "hunk-to-source": "[> Current → Source]"}[id]) {
			t.Fatalf("hunk region not on rendered button: %s", line)
		}
	}
	click(m, hitOf(t, m, "hunk-action", "next-hunk"))
	if s.hunkIndex != 1 {
		t.Fatal("mouse Next did not select next hunk")
	}
	s.previewLoading = false
	click(m, hitOf(t, m, "hunk-action", "hunk-to-current"))
	if m.dialog == nil || m.dialog.confirm.direction != "source-to-current" || f.copies != 0 {
		t.Fatal("mouse copy bypassed or misdirected review")
	}
	cmd := click(m, hitOf(t, m, "dialog", "confirm"))
	m.Update(cmd())
	if f.copies != 1 || f.direction != "source-to-current" {
		t.Fatal("mouse Confirm copied wrong direction")
	}
}

func TestDiffLabelsIdentifyBothSidesAndRenderer(t *testing.T) {
	m, f := featureFixture()
	s := &m.lists[0]
	s.view = 3
	s.snapshot = f.snapshot
	s.renderName = "delta"
	s.renderLayout = "side-by-side"
	view := strings.Join(m.detailPane(120, 20), "\n")
	for _, text := range []string{"Before: Current", "After: Source", "Renderer: delta · side-by-side"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %s", text)
		}
	}
	s.snapshot = nil
	s.renderLayout = "unified"
	s.renderName = "builtin"
	s.renderWarning = "delta unavailable; builtin fallback"
	view = strings.Join(m.detailPane(100, 20), "\n")
	if !strings.Contains(view, "After: Rendered") || !strings.Contains(view, "builtin fallback") {
		t.Fatal("special diff or fallback mislabeled")
	}
}

func TestDiffResizeOnlyRendersCachedRaw(t *testing.T) {
	m, f := featureFixture()
	s := &m.lists[0]
	s.view = 3
	s.previewID = "a"
	s.previewGen = 7
	s.rawDiff = "original raw"
	renders := 0
	width := 0
	m.renderer = func(_ context.Context, raw string, opts diffview.Options) (diffview.Result, error) {
		renders++
		width = opts.Width
		if raw != "original raw" {
			t.Fatal("renderer received transformed text")
		}
		return diffview.Result{Text: "rendered"}, nil
	}
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 180, Height: 40})
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, effect := range msg {
			if effect != nil {
				m.Update(effect())
			}
		}
	default:
		m.Update(msg)
	}
	if renders != 1 || f.calls != 0 || f.hunkReads != 0 || width != 118 || s.preview != "rendered" {
		t.Fatalf("resize reread backend or wrong width: render=%d read=%d width=%d", renders, f.calls, width)
	}
	gen := s.renderGen
	press(m, "z")
	m.Update(diffRenderedMsg{tab: 0, gen: gen, previewGen: s.previewGen, result: diffview.Result{Text: "obsolete"}})
	if s.preview == "obsolete" {
		t.Fatal("late narrow render replaced maximized preview")
	}
}

func TestHunkCopyNeedsExactReviewAndUndoUsesReceipt(t *testing.T) {
	m, f := featureFixture()
	s := &m.lists[0]
	s.view = 3
	s.snapshot = f.snapshot
	s.hunkMode = true
	s.previewID = "a"
	press(m, "n")
	if s.hunkIndex != 1 {
		t.Fatal("next hunk failed")
	}
	s.previewLoading = false
	press(m, ">")
	if m.dialog == nil || m.pending != nil || f.copies != 0 {
		t.Fatal("copy bypassed review")
	}
	view := strings.Join(m.dialogView(100, 24), "\n")
	if !strings.Contains(view, "/src/dot_a") || !strings.Contains(view, "-old2") || !strings.Contains(view, "Current → Source") {
		t.Fatalf("review omitted exact direction/path/patch: %s", view)
	}
	cmd := click(m, hitOf(t, m, "dialog", "confirm"))
	message := cmd()
	_, done := m.Update(message)
	m.Update(done())
	if f.copies != 1 || f.direction != "current-to-source" || m.lastCopy == nil {
		t.Fatal("copy receipt missing")
	}
	cmd = press(m, "U")
	_, done = m.Update(cmd())
	m.Update(done())
	if f.undos != 1 || m.lastCopy != nil {
		t.Fatal("undo failed to consume receipt")
	}
}

func TestSearchDebounceOwnsTextAndPreservesFilenameFilter(t *testing.T) {
	m, _ := featureFixture()
	f := m.searcher.(*fakeSearch)
	m.lists[0].query = ".b"
	m.lists[0].selected = 0
	press(m, "s")
	for _, k := range []string{"m", "j", "q", "/", "?"} {
		press(m, k)
	}
	if m.search.text != "mjq/?" || !m.opts.Mouse || m.pending != nil || m.dialog == nil {
		t.Fatal("search input dispatched printable shortcuts")
	}
	old := m.search.gen
	press(m, "x")
	_, cmd := m.Update(searchDebounceMsg{gen: old})
	if cmd != nil || f.calls != 0 {
		t.Fatal("outdated debounce started search")
	}
	cmd = press(m, "enter")
	if m.dialog != nil || m.detail || m.pending != nil {
		t.Fatal("Enter acted instead of accepting query")
	}
	m.Update(cmd())
	if f.calls != 1 || f.queries[0].Text != "mjq/?x" {
		t.Fatal("Enter did not flush latest query")
	}
	press(m, "1")
	if m.lists[0].query != ".b" {
		t.Fatal("content search destroyed filename filter")
	}
}

func TestSearchRejectsOldResultsAndRetainsFailedQueryContext(t *testing.T) {
	m, _ := featureFixture()
	m.tab = 3
	m.search.text = "new"
	m.search.gen = 5
	match := search.Match{Path: "/repo/config", Relative: "config", Scope: "source", Line: 2, Column: 8, Text: "second answer", Spans: []search.Span{{Start: 7, End: 13}}}
	m.Update(searchResultsMsg{gen: 5, query: search.Query{Scope: "source", Text: "new"}, result: search.Result{Matches: []search.Match{match}}})
	m.Update(searchResultsMsg{gen: 4, err: errors.New("old failure")})
	if m.search.err != "" {
		t.Fatal("old error overwrote search")
	}
	m.search.text = "["
	m.search.regex = true
	m.search.gen = 6
	m.Update(searchResultsMsg{gen: 6, err: errors.New("invalid regex")})
	if len(m.search.result.Matches) != 1 || !m.search.stale {
		t.Fatal("error lost useful previous results")
	}
	view := strings.Join(m.listPane(80, 20), "\n")
	if !strings.Contains(view, "Results for: new (source)") {
		t.Fatal("old results mislabelled as new query")
	}
}

func TestSearchPreviewAndUnmanagedEditorRouting(t *testing.T) {
	m, f := featureFixture()
	m.tab = 3
	m.search.result.Matches = []search.Match{{Path: "/repo/.chezmoitemplates/helper", Relative: ".chezmoitemplates/helper", Scope: "source", Line: 2, Column: 8, Text: "second answer"}}
	cmd := m.loadSearchPreview()
	m.Update(cmd())
	if !strings.Contains(m.search.preview, "second answer") || m.search.scroll != 0 {
		t.Fatal("search preview failed")
	}
	cmd = press(m, "e")
	cmd()
	if f.path != "/repo/.chezmoitemplates/helper" {
		t.Fatal("unmanaged hit did not use exact source-file editor")
	}
	for _, a := range m.actions() {
		if a.id == "apply" && a.enabled {
			t.Fatal("unmanaged search result offered apply")
		}
	}
}

func TestNewViewsFitNarrowScreens(t *testing.T) {
	m, _ := featureFixture()
	m.tab = 3
	m.search.text = "專案 👩🏽‍💻"
	m.search.result.Matches = []search.Match{{Path: "/repo/專案", Relative: "專案", Line: 1, Scope: "source", Text: "hello"}}
	for _, size := range [][2]int{{180, 40}, {80, 24}, {55, 18}, {5, 3}, {1, 1}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, detail := range []bool{false, true} {
			m.detail = detail
			v := m.View().Content
			for _, line := range strings.Split(v, "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("overflow %v: %q", size, line)
				}
			}
		}
	}
}

func TestAutomaticThemeRespondsWithoutRereadingFiles(t *testing.T) {
	m, f := featureFixture()
	s := &m.lists[0]
	s.view = 3
	s.rawDiff = "cached raw"
	s.previewID = "a"
	themes := []string{}
	m.renderer = func(_ context.Context, raw string, opts diffview.Options) (diffview.Result, error) {
		if raw != "cached raw" {
			t.Fatal("lost raw snapshot")
		}
		themes = append(themes, opts.Theme)
		return diffview.Result{Text: "rendered", Renderer: "builtin", Layout: "unified"}, nil
	}
	_, cmd := m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 255, G: 255, B: 255, A: 255}})
	if cmd == nil || f.calls != 0 || f.hunkReads != 0 {
		t.Fatal("theme did I/O in Update or failed to rerender")
	}
	m.Update(cmd())
	if len(themes) != 1 || themes[0] != "light" {
		t.Fatalf("auto theme did not detect light background: %v", themes)
	}
	_, cmd = m.Update(tea.BackgroundColorMsg{Color: color.RGBA{A: 255}})
	m.Update(cmd())
	if themes[1] != "dark" || f.calls != 0 || f.hunkReads != 0 {
		t.Fatal("dark theme change reread backend")
	}
	_, cmd = m.Update(tea.BackgroundColorMsg{Color: color.RGBA{A: 255}})
	if cmd != nil {
		t.Fatal("unchanged background rerendered needlessly")
	}
}

func TestExplicitThemeIgnoresBackgroundReport(t *testing.T) {
	for _, theme := range []string{"light", "dark"} {
		m, _ := featureFixture()
		m.opts.Diff.Theme = theme
		_, cmd := m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 255, G: 255, B: 255, A: 255}})
		if cmd != nil {
			t.Fatalf("explicit %s responded to auto background", theme)
		}
	}
}
