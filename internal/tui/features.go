package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
	"github.com/daviddwlee84/lazychezmoi/internal/diffview"
	"github.com/daviddwlee84/lazychezmoi/internal/search"
)

type hunkBackend interface {
	Hunks(context.Context, chezmoi.Entry) (*chezmoi.DiffSnapshot, error)
	CopyHunk(context.Context, *chezmoi.DiffSnapshot, string, string) (*chezmoi.CopyReceipt, error)
	UndoCopy(context.Context, *chezmoi.CopyReceipt) error
}
type searchBackend interface {
	Search(context.Context, *chezmoi.Service, search.Query) (search.Result, error)
	Preview(context.Context, search.Match) (string, error)
}
type searchEditor interface {
	EditSearchFile(context.Context, string) (*exec.Cmd, error)
}

type searchState struct {
	text, scope              string
	regex                    bool
	result                   search.Result
	resultQuery              search.Query
	selected, scroll         int
	loading, stale           bool
	err, preview, previewErr string
	previewWarning           string
	previewLoading           bool
	gen, previewGen          uint64
	cancel, previewCancel    context.CancelFunc
}

type diffRenderedMsg struct {
	tab             int
	gen, previewGen uint64
	result          diffview.Result
	err             error
}
type searchDebounceMsg struct{ gen uint64 }
type searchResultsMsg struct {
	gen    uint64
	query  search.Query
	result search.Result
	err    error
}
type searchPreviewMsg struct {
	gen          uint64
	key, content string
	err          error
	warning      string
}
type copyMsg struct {
	id      uint64
	receipt *chezmoi.CopyReceipt
	undo    bool
	err     error
}

func hunkCount(s *listState) int {
	if s.snapshot == nil {
		return 0
	}
	return len(s.snapshot.Hunks)
}

func (m *model) readDiff(ctx context.Context, tab int, gen uint64, e chezmoi.Entry) tea.Msg {
	msg := previewMsg{tab: tab, gen: gen, id: e.ID, view: "diff"}
	if h, ok := m.service.(hunkBackend); ok {
		snapshot, err := h.Hunks(ctx, e)
		if err == nil && snapshot != nil {
			msg.snapshot = snapshot
			msg.content = snapshot.Raw
			return msg
		}
		if err != nil {
			msg.hunkReason = err.Error()
		}
	} else {
		msg.hunkReason = "Hunk copying unavailable"
	}
	msg.content, msg.err = m.service.Preview(ctx, e, "diff")
	return msg
}

func (m *model) renderCachedDiff(tab int) tea.Cmd {
	s := &m.lists[tab]
	if s.view != 3 {
		return nil
	}
	if s.renderCancel != nil {
		s.renderCancel()
	}
	s.renderGen++
	s.previewLoading = true
	ctx, cancel := context.WithCancel(m.ctx)
	s.renderCancel = cancel
	raw := s.rawDiff
	if s.hunkMode && hunkCount(s) > 0 {
		raw = s.snapshot.Hunks[min(s.hunkIndex, hunkCount(s)-1)].Patch
	}
	opts := m.opts.Diff
	opts.Color = m.opts.Color
	opts.Width = m.previewWidth()
	if opts.Theme == "" || opts.Theme == "auto" {
		opts.Theme = "dark"
		if !m.backgroundDark {
			opts.Theme = "light"
		}
	}
	gen, previewGen, renderer := s.renderGen, s.previewGen, m.renderer
	return func() tea.Msg {
		defer cancel()
		r, err := renderer(ctx, raw, opts)
		return diffRenderedMsg{tab, gen, previewGen, r, err}
	}
}

func (m *model) rerenderDiffs() tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.lists {
		if m.lists[i].view == 3 && m.lists[i].previewID != "" {
			cmds = append(cmds, m.renderCachedDiff(i))
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) previewWidth() int {
	width := m.width
	if width >= 80 && !m.maximized {
		width -= max(26, width/3)
	}
	return max(1, width-2)
}

func (m *model) toggleHunks() tea.Cmd {
	if m.tab != 0 {
		return nil
	}
	s := &m.lists[0]
	s.hunkMode = !s.hunkMode
	s.scroll = 0
	m.detail = true
	if s.view != 3 {
		s.view = 3
		return m.loadPreview()
	}
	return m.renderCachedDiff(0)
}

func (m *model) selectHunk(delta int) tea.Cmd {
	s := &m.lists[0]
	next := max(0, min(s.hunkIndex+delta, hunkCount(s)-1))
	if next == s.hunkIndex {
		return nil
	}
	s.hunkIndex = next
	s.scroll = 0
	return m.renderCachedDiff(0)
}

func (m *model) reviewHunk(direction string) tea.Cmd {
	s := &m.lists[0]
	if s.snapshot == nil || hunkCount(s) == 0 || s.stale || s.previewLoading {
		return nil
	}
	h := s.snapshot.Hunks[s.hunkIndex]
	label := "Copy hunk Source → Current"
	if direction == "current-to-source" {
		label = "Copy hunk Current → Source"
	}
	op := &pendingOperation{label: label, entry: s.snapshot.Entry, snapshot: s.snapshot, hunkID: h.ID, direction: direction}
	m.dialog = &dialog{kind: "confirm", confirm: op}
	m.pressed = nil
	return nil
}

func (m *model) prepareFeatureOperation(op pendingOperation) (tea.Cmd, bool) {
	ctx, service := m.ctx, m.service
	if op.snapshot != nil || op.undo != nil {
		return func() tea.Msg {
			h, ok := service.(hunkBackend)
			if !ok {
				return copyMsg{id: op.id, err: fmt.Errorf("hunk operations unavailable")}
			}
			if op.undo != nil {
				return copyMsg{id: op.id, undo: true, err: h.UndoCopy(ctx, op.undo)}
			}
			r, err := h.CopyHunk(ctx, op.snapshot, op.hunkID, op.direction)
			return copyMsg{id: op.id, receipt: r, err: err}
		}, true
	}
	if op.searchPath != "" {
		return func() tea.Msg {
			editor, ok := service.(searchEditor)
			if !ok {
				return preparedMsg{id: op.id, err: fmt.Errorf("source editor unavailable")}
			}
			cmd, err := editor.EditSearchFile(ctx, op.searchPath)
			return preparedMsg{op.id, cmd, err}
		}, true
	}
	return nil, false
}

func (m *model) featureMessage(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		if msg.Color == nil || (m.opts.Diff.Theme != "" && m.opts.Diff.Theme != "auto") {
			return nil, true
		}
		dark := msg.IsDark()
		if dark == m.backgroundDark {
			return nil, true
		}
		m.backgroundDark = dark
		return m.rerenderDiffs(), true
	case tea.MouseMsg:
		return m.mouse(msg), true
	case diffRenderedMsg:
		s := &m.lists[msg.tab]
		if msg.gen != s.renderGen || msg.previewGen != s.previewGen || s.view != 3 {
			return nil, true
		}
		s.previewLoading = false
		if msg.err != nil {
			s.previewErr = msg.err.Error()
		} else {
			s.preview = msg.result.Text
			s.previewErr = ""
			s.renderWarning = msg.result.Warning
			s.renderLayout = msg.result.Layout
			s.renderName = msg.result.Renderer
		}
		return nil, true
	case copyMsg:
		if m.pending == nil || msg.id != m.pending.id {
			return nil, true
		}
		if msg.err == nil {
			if msg.undo {
				m.lastCopy = nil
			} else {
				m.lastCopy = msg.receipt
			}
		}
		return m.finishOperation(msg.id, msg.err), true
	case searchDebounceMsg:
		if msg.gen != m.search.gen {
			return nil, true
		}
		return m.startSearch(), true
	case searchResultsMsg:
		s := &m.search
		if msg.gen != s.gen {
			return nil, true
		}
		s.loading = false
		m.pressed = nil
		if msg.err != nil && len(msg.result.Matches) == 0 {
			s.err = msg.err.Error()
			if len(msg.result.Errors) > 0 {
				s.err += "; " + strings.Join(msg.result.Errors, "; ")
			}
			s.stale = len(s.result.Matches) > 0
			return nil, true
		}
		old := m.selectedMatch()
		key := ""
		if old != nil {
			key = matchKey(*old)
		}
		s.result = msg.result
		s.resultQuery = msg.query
		s.err = ""
		s.stale = false
		if msg.err != nil {
			s.err = msg.err.Error()
		}
		s.selected = min(s.selected, max(0, len(s.result.Matches)-1))
		for i, match := range s.result.Matches {
			if matchKey(match) == key {
				s.selected = i
				break
			}
		}
		return m.loadSearchPreview(), true
	case searchPreviewMsg:
		s := &m.search
		selected := m.selectedMatch()
		if msg.gen != s.previewGen || selected == nil || msg.key != matchKey(*selected) {
			return nil, true
		}
		s.previewLoading = false
		s.previewWarning = msg.warning
		if msg.err != nil {
			s.previewErr = msg.err.Error()
		} else {
			s.preview = msg.content
			s.previewErr = ""
			s.scroll = max(0, selected.Line-3)
		}
		return nil, true
	}
	return nil, false
}

func (m *model) switchTab(tab int) tea.Cmd {
	m.pressed = nil
	m.tab = tab
	m.detail = false
	m.maximized = false
	if tab < 2 {
		if !m.lists[tab].loaded {
			return m.loadEntries(tab)
		}
		return m.loadPreview()
	}
	if tab == 3 {
		return m.loadSearchPreview()
	}
	return nil
}

func (m *model) cancelSearch() {
	s := &m.search
	s.gen++
	s.previewGen++
	if s.cancel != nil {
		s.cancel()
	}
	if s.previewCancel != nil {
		s.previewCancel()
	}
	s.loading = false
	s.previewLoading = false
}

func (m *model) scheduleSearch() tea.Cmd {
	m.cancelSearch()
	s := &m.search
	s.stale = len(s.result.Matches) > 0
	s.loading = s.text != ""
	if s.text == "" {
		s.result = search.Result{}
		s.preview = ""
		s.err = ""
		s.stale = false
		return nil
	}
	gen := s.gen
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return searchDebounceMsg{gen} })
}

func (m *model) startSearch() tea.Cmd {
	s := &m.search
	if s.text == "" {
		return nil
	}
	if m.pending != nil {
		s.stale = true
		s.loading = false
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.gen++
	s.loading = true
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	gen, q, searcher, owner := s.gen, search.Query{Scope: s.scope, Text: s.text, Regex: s.regex}, m.searcher, m.owner
	return func() tea.Msg {
		defer cancel()
		r, e := searcher.Search(ctx, owner, q)
		return searchResultsMsg{gen, q, r, e}
	}
}

func matchKey(match search.Match) string {
	return fmt.Sprintf("%s:%s:%d:%d", match.Scope, match.Path, match.Line, match.Column)
}
func (m *model) selectedMatch() *search.Match {
	s := &m.search
	if s.selected < 0 || s.selected >= len(s.result.Matches) {
		return nil
	}
	match := s.result.Matches[s.selected]
	return &match
}

func (m *model) loadSearchPreview() tea.Cmd {
	s := &m.search
	if s.previewCancel != nil {
		s.previewCancel()
	}
	s.previewGen++
	match := m.selectedMatch()
	if match == nil {
		s.preview = ""
		s.previewLoading = false
		return nil
	}
	s.previewLoading = true
	s.previewErr = ""
	s.previewWarning = ""
	s.preview = ""
	ctx, cancel := context.WithCancel(m.ctx)
	s.previewCancel = cancel
	gen, searcher, color := s.previewGen, m.searcher, m.opts.Color
	return func() tea.Msg {
		defer cancel()
		body, err := searcher.Preview(ctx, *match)
		warning := ""
		if err == nil {
			rawLines := strings.Split(body, "\n")
			e := chezmoi.Entry{Target: match.Path, Source: match.Path}
			if match.Entry != nil {
				e = *match.Entry
			}
			body = highlight(body, e, match.Scope, color)
			if match.Line > 0 && match.Line <= len(rawLines) {
				rawLine := strings.TrimSuffix(rawLines[match.Line-1], "\r")
				if rawLine == strings.TrimSuffix(match.Text, "\n") {
					lines := strings.Split(body, "\n")
					if match.Line <= len(lines) {
						lines[match.Line-1] = highlightMatch(rawLine, match.Spans, color)
						body = strings.Join(lines, "\n")
					}
				} else {
					warning = "File changed since search; r searches again"
				}
			} else {
				warning = "File changed since search; r searches again"
			}
		}
		return searchPreviewMsg{gen, matchKey(*match), body, err, warning}
	}
}

func (m *model) moveSearch(delta int) tea.Cmd {
	s := &m.search
	if m.detail {
		s.scroll = max(0, min(s.scroll+delta, strings.Count(s.preview, "\n")))
		return nil
	}
	next := max(0, min(s.selected+delta, len(s.result.Matches)-1))
	if next == s.selected {
		return nil
	}
	s.selected = next
	return m.loadSearchPreview()
}

func (m *model) searchEdit() tea.Cmd {
	match := m.selectedMatch()
	if match == nil {
		return nil
	}
	op := &pendingOperation{label: "Edit search result source"}
	if match.Entry != nil {
		op.entry = *match.Entry
		op.op = chezmoi.Operation{Kind: "edit", Targets: []string{match.Entry.Source}, ExcludeScripts: !strings.HasPrefix(match.Entry.Kind, "script")}
	} else {
		op.searchPath = match.Path
	}
	return m.beginOperation(op)
}
