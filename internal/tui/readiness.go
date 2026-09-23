package tui

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

type statusMsg struct {
	tab               int
	gen, inventoryGen uint64
	statuses          map[string]chezmoi.EntryStatus
	err               error
}
type refreshReadyMsg struct{ gen uint64 }

// Epoch publication is nonblocking in Update. Only workers hold mu, serializing
// the check and cache invalidation so a delayed obsolete effect cannot cancel
// discovery belonging to a newer refresh or a completed mutation.
type invalidationGate struct {
	latest atomic.Uint64
	mu     sync.Mutex
}

func (g *invalidationGate) invalidate(epoch uint64, service backend) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if epoch != g.latest.Load() {
		return false
	}
	service.Invalidate()
	return true
}

func (g *invalidationGate) fence(epoch uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return epoch == g.latest.Load()
}

func selectedID(s *listState) string {
	rows := s.visible()
	if s.selected >= 0 && s.selected < len(rows) {
		return rows[s.selected].ID
	}
	return ""
}
func restoreSelection(s *listState, id string) {
	rows := s.visible()
	s.selected = max(0, min(s.selected, len(rows)-1))
	for i, row := range rows {
		if row.ID == id {
			s.selected = i
			break
		}
	}
}

func (m *model) loadStatus(tab int) tea.Cmd {
	s := &m.lists[tab]
	if s.statusCancel != nil {
		s.statusCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	s.statusCancel = cancel
	s.statusGen++
	s.statusLoading = true
	s.statusStale = s.statusKnown
	gen, inventoryGen, service := s.statusGen, s.gen, m.service
	return func() tea.Msg {
		defer cancel()
		statuses, err := service.Status(ctx, tab == 1)
		return statusMsg{tab, gen, inventoryGen, statuses, err}
	}
}

// Inventory is authoritative about membership. It is ready for navigation and
// source previews independently of the expensive native change-status scan.
func (m *model) acceptInventory(msg entriesMsg) tea.Cmd {
	s := &m.lists[msg.tab]
	if msg.gen != s.gen {
		return nil
	}
	m.pressed = nil
	s.loading = false
	var cmds []tea.Cmd
	if m.opts.AutoFetch && !m.autoFetchStarted {
		m.autoFetchStarted = true
		cmds = append(cmds, m.fetch())
	}
	if msg.err != nil && len(msg.entries) == 0 {
		s.err = msg.err.Error()
		s.stale = s.loaded
		return tea.Batch(cmds...)
	}
	id := selectedID(s)
	old := s.entries
	incoming := make(map[string]chezmoi.Entry, len(msg.entries))
	for _, entry := range msg.entries {
		entry.Drift = "?"
		entry.Pending = "?"
		incoming[entry.ID] = entry
	}
	// Preserve existing order and previously observed status while refreshing;
	// the stale status is explicitly labelled until this generation completes.
	entries := make([]chezmoi.Entry, 0, len(msg.entries))
	for _, previous := range old {
		entry, ok := incoming[previous.ID]
		if !ok {
			continue
		}
		if s.statusKnown && entry.Source == previous.Source && entry.Kind == previous.Kind {
			entry.Drift = previous.Drift
			entry.Pending = previous.Pending
		}
		entries = append(entries, entry)
		delete(incoming, entry.ID)
	}
	for _, entry := range msg.entries {
		if item, ok := incoming[entry.ID]; ok {
			entries = append(entries, item)
			delete(incoming, entry.ID)
		}
	}
	s.entries = entries
	s.loaded = true
	s.err = ""
	s.stale = false
	s.sortPending = true
	if msg.err != nil {
		s.err = msg.err.Error()
		s.stale = true
	}
	restoreSelection(s, id)
	valid := make(map[string]bool, len(entries))
	for _, entry := range entries {
		valid[entry.ID] = true
	}
	for id := range s.checked {
		if !valid[id] {
			delete(s.checked, id)
		}
	}
	if m.status == "Loading local source…" {
		m.status = "Ready · checking changes"
	}
	if m.tab == msg.tab {
		cmds = append(cmds, m.loadPreview())
	}
	cmds = append(cmds, m.loadStatus(msg.tab))
	return tea.Batch(cmds...)
}

func (m *model) readinessMessage(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case refreshReadyMsg:
		if msg.gen != m.refreshGen || m.pending != nil {
			return nil, true
		}
		cmds := []tea.Cmd{m.reloadViews()}
		if m.search.text != "" {
			cmds = append(cmds, m.startSearch())
		}
		return tea.Batch(cmds...), true
	case statusMsg:
		s := &m.lists[msg.tab]
		if msg.gen != s.statusGen || msg.inventoryGen != s.gen {
			return nil, true
		}
		s.statusLoading = false
		if msg.err != nil {
			s.statusErr = msg.err.Error()
			s.statusStale = s.statusKnown
			return nil, true
		}
		id := selectedID(s)
		for i := range s.entries {
			status := msg.statuses[s.entries[i].Relative]
			s.entries[i].Drift = status.Drift
			s.entries[i].Pending = status.Pending
		}
		s.statusKnown = true
		s.statusStale = false
		s.statusErr = ""
		s.sortPending = true
		restoreSelection(s, id)
		if selectedID(s) != id {
			m.pressed = nil
		}
		if m.status == "Ready · checking changes" {
			m.status = "Ready"
		}
		// Status-only enrichment must not re-evaluate a selected template or
		// recreate hunk snapshots. Reload only if changed-only removed selection.
		if m.tab == msg.tab && selectedID(s) != id {
			return m.loadPreview(), true
		}
		return nil, true
	}
	return nil, false
}

// Reordering happens after the message has fully handled input: releasing a
// button first acts on the row that was pressed, then sorting preserves its ID.
func (m *model) flushPendingSorts() tea.Cmd {
	if m.dialog != nil || m.pressed != nil {
		return nil
	}
	var cmds []tea.Cmd
	for tab := range m.lists {
		s := &m.lists[tab]
		if !s.sortPending {
			continue
		}
		id := selectedID(s)
		sort.SliceStable(s.entries, func(i, j int) bool {
			a, b := s.entries[i], s.entries[j]
			if entryChanged(a) != entryChanged(b) {
				return entryChanged(a)
			}
			return a.Relative < b.Relative
		})
		s.sortPending = false
		restoreSelection(s, id)
		if tab == m.tab && selectedID(s) != id {
			cmds = append(cmds, m.loadPreview())
		}
	}
	return tea.Batch(cmds...)
}
