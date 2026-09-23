package tui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

// The registry is the common source for dispatch, palette, footer and help.
type action struct {
	id, key, label         string
	enabled, write, footer bool
}

var maintenanceActions = []string{"init", "init-prompt", "edit-config", "edit-config-template", "source", "externals", "update-reload", "reload", "context", "results"}

func (m *model) actions() []action {
	e, selected := m.selectedEntry()
	file := m.tab == 0 && selected
	script := m.tab == 1 && selected
	idle := m.pending == nil
	match := m.selectedMatch()
	hunks := m.tab == 0 && m.lists[0].view == 3 && m.lists[0].hunkMode
	canCopy := hunks && hunkCount(&m.lists[0]) > 0 && !m.lists[0].previewLoading && !m.lists[0].stale && idle
	hasChecked := false
	for _, checked := range m.lists[0].checked {
		if checked {
			hasChecked = true
			break
		}
	}
	return []action{
		{"mouse", "m", "Toggle mouse capture", true, false, false},
		{"search", "s", "Search file contents", true, false, true},
		{"search-edit", "e", "Edit search result source", m.tab == 3 && match != nil && idle, true, true},
		{"search-scope", "", "Toggle Source / Current search", m.tab == 3, false, false},
		{"search-regex", "", "Toggle regex search", m.tab == 3, false, false},
		{"maximize", "z", "Maximize / restore preview", m.tab != 2, false, false},
		{"hunks", "H", "Toggle hunk picker", file, false, true},
		{"next-hunk", "n", "Next hunk", hunks && hunkCount(&m.lists[0]) > 0, false, false},
		{"prev-hunk", "N", "Previous hunk", hunks && hunkCount(&m.lists[0]) > 0, false, false},
		{"hunk-to-current", "<", "Copy selected hunk Source → Current", canCopy, true, false},
		{"hunk-to-source", ">", "Copy selected hunk Current → Source", canCopy, true, false},
		{"undo-copy", "U", "Undo last hunk copy", m.lastCopy != nil && idle, true, false},
		{"edit", "e", "Edit source in editor", (file || script) && idle, true, true},
		{"edit-apply", "", "Edit source & apply", file && idle, true, false},
		{"apply", "a", "Apply selected files (exclude scripts)", m.tab == 0 && (selected || hasChecked) && idle, true, true},
		{"script-apply", "a", "Apply selected script", script && idle, true, true},
		{"apply-all", "A", "Apply all (including scripts)", idle, true, false},
		{"fetch", "f", "Fetch remote updates", idle && !m.fetching, false, true},
		{"fetch-interactive", "", "Fetch interactively (allow authentication)", idle && !m.fetching, true, false},
		{"update", "u", "Update & init", idle, true, true},
		{"update-no-init", "", "Update without init", idle, true, false},
		{"lazygit", "L", "Open lazygit", idle, true, true},
		{"refresh", "r", "Refresh local state", idle, false, false},
		{"filter", "/", "Filter targets", m.tab < 2, false, false},
		{"changed", "c", "Toggle changed files only", m.tab < 2 && m.lists[m.tab].statusKnown, false, false},
		{"select", "space", "Toggle selection", file, false, false},
		{"clear-selection", "", "Clear selected files", m.tab == 0, false, false},
		{"next-preview", "v", "Next preview (Source / Current / Rendered / Diff)", m.tab < 2, false, false},
		{"prev-preview", "[", "Previous preview", m.tab < 2, false, false},
		{"next-preview", "]", "Next preview", m.tab < 2, false, false},
		{"re-add", "", "Absorb local edits (re-add)", file && e.Kind == "file" && !e.Template && !e.Encrypted && !e.ExactAncestor && idle, true, false},
		{"reset-script", "x", "Reset exact script record", script && idle, true, false},
		{"reset-apply-script", "X", "Reset record & apply script", script && idle, true, false},
		{"init", "", "Init parameters", idle, true, false},
		{"init-prompt", "", "Re-prompt existing parameters", idle, true, false},
		{"edit-config", "", "Edit chezmoi config", idle, true, false},
		{"edit-config-template", "", "Edit config template", idle, true, false},
		{"source", "", "Open source tree in editor", idle, true, false},
		{"externals", "", "Refresh & apply externals (exclude scripts)", idle, true, false},
		{"update-reload", "", "Update & reload shell on exit", idle, true, false},
		{"reload", "", "Reload shell & exit", idle, true, false},
		{"results", "", "Recent operation results", true, false, false},
		{"context", "", "Active source, destination & paths", true, false, false},
		{"palette", ":", "Actions", true, false, true},
		{"help", "?", "Help", true, false, true},
	}
}

func (m *model) dispatch(id string) tea.Cmd {
	allowed := false
	for _, a := range m.actions() {
		if a.id == id && a.enabled {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil
	}
	e, _ := m.selectedEntry()
	operation := func(kind, label string, targets []string) *pendingOperation {
		return &pendingOperation{label: label, op: chezmoi.Operation{Kind: kind, Targets: targets}, entry: e}
	}
	switch id {
	case "mouse":
		m.opts.Mouse = !m.opts.Mouse
		m.pressed = nil
		if m.opts.Mouse {
			m.report("Mouse capture enabled · m disables for text selection", false)
		} else {
			m.report("Mouse capture disabled · m enables", false)
		}
		return nil
	case "search":
		m.tab = 3
		m.detail = false
		m.maximized = false
		return m.openInput("search")
	case "search-edit":
		return m.searchEdit()
	case "search-scope":
		if m.search.scope == "source" {
			m.search.scope = "current"
		} else {
			m.search.scope = "source"
		}
		return m.scheduleSearch()
	case "search-regex":
		m.search.regex = !m.search.regex
		return m.scheduleSearch()
	case "maximize":
		m.maximized = !m.maximized
		if m.maximized {
			m.detail = true
		}
		m.pressed = nil
		return m.rerenderDiffs()
	case "hunks":
		return m.toggleHunks()
	case "next-hunk":
		return m.selectHunk(1)
	case "prev-hunk":
		return m.selectHunk(-1)
	case "hunk-to-current":
		return m.reviewHunk("source-to-current")
	case "hunk-to-source":
		return m.reviewHunk("current-to-source")
	case "undo-copy":
		return m.beginOperation(&pendingOperation{label: "Undo hunk copy", undo: m.lastCopy})
	case "filter":
		return m.openInput("filter")
	case "palette":
		return m.openInput("palette")
	case "help", "results", "context":
		m.dialog = &dialog{kind: id}
		return nil
	case "changed":
		s := &m.lists[m.tab]
		s.changed = !s.changed
		s.selected = 0
		s.scroll = 0
		return m.loadPreview()
	case "select":
		m.lists[0].checked[e.ID] = !m.lists[0].checked[e.ID]
		return nil
	case "clear-selection":
		m.lists[0].checked = make(map[string]bool)
		return nil
	case "next-preview", "prev-preview":
		s := &m.lists[m.tab]
		delta := 1
		if id == "prev-preview" {
			delta = -1
		}
		s.view = (s.view + delta + len(previewViews)) % len(previewViews)
		s.scroll = 0
		s.preview = ""
		return m.loadPreview()
	case "fetch":
		return m.fetch()
	case "fetch-interactive":
		return m.beginOperation(operation("fetch", "Fetch", nil))
	case "refresh":
		return m.refresh()
	case "edit", "edit-apply":
		op := operation("edit", "Edit source", []string{e.Target})
		if m.tab == 1 {
			op.op.Targets = []string{e.Source}
		}
		op.op.Apply = id == "edit-apply"
		op.op.ExcludeScripts = m.tab == 0
		return m.beginOperation(op)
	case "apply":
		op := operation("apply", "Apply selected files", m.targets())
		op.op.ExcludeScripts = true
		return m.beginOperation(op)
	case "apply-all":
		m.dialog = &dialog{kind: "confirm", confirm: operation("apply", "Apply ALL managed files and scripts", nil)}
		return nil
	case "script-apply":
		return m.beginOperation(operation("script-apply", "Apply script", []string{e.Source}))
	case "reset-script":
		return m.loadRecords(false)
	case "reset-apply-script":
		return m.loadRecords(true)
	case "re-add":
		m.dialog = &dialog{kind: "confirm", confirm: operation("re-add", "Absorb local edits into source", []string{e.Target})}
		return nil
	case "update", "update-no-init", "update-reload":
		op := operation("update", "Update & init", nil)
		op.op.Init = id != "update-no-init"
		op.op.Apply = true
		if !op.op.Init {
			op.label = "Update without init"
		}
		if id == "update-reload" {
			op.reload = true
			return m.reloadCommand(op)
		}
		return m.beginOperation(op)
	case "reload":
		return m.reloadCommand(nil)
	case "init", "init-prompt":
		op := operation("init", "Init parameters", nil)
		op.op.Prompt = id == "init-prompt"
		return m.beginOperation(op)
	case "lazygit", "edit-config", "edit-config-template", "source", "externals":
		label := id
		for _, a := range m.actions() {
			if a.id == id {
				label = a.label
				break
			}
		}
		op := operation(id, label, nil)
		if id == "externals" {
			op.op.ExcludeScripts = true
		}
		return m.beginOperation(op)
	}
	return nil
}

// Plain text styles are used when the caller explicitly disables color.
func plainInputStyles() textinput.Styles { return textinput.Styles{} }
