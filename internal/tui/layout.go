package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type rect struct{ X, Y, W, H int }

func (r rect) contains(x, y int) bool {
	return r.W > 0 && r.H > 0 && x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

type hitTarget struct {
	Rect     rect
	Kind, ID string
	Index    int
}
type rowHit struct {
	Line, Index int
	ID          string
}
type paneContent struct {
	Title string
	Lines []string
	Rows  []rowHit
}
type screenLayout struct {
	Body, List, Detail rect
	Hits               []hitTarget
}

func intersect(a, b rect) rect {
	x, y := max(a.X, b.X), max(a.Y, b.Y)
	return rect{x, y, max(0, min(a.X+a.W, b.X+b.W)-x), max(0, min(a.Y+a.H, b.Y+b.H)-y)}
}

func (m *model) tabLabels() []string {
	labels := []string{"1 Files", "2 Scripts", "3 Maintenance", "4 Search"}
	for i := range labels {
		if m.tab == i {
			labels[i] = "[" + labels[i] + "]"
		}
	}
	return labels
}

// Layout is a pure function of model state. Both rendering and event routing use
// the same pane bounds and visible row plan; View never installs hit regions.
func (m *model) layout() screenLayout {
	l := screenLayout{Body: rect{0, 3, m.width, max(1, m.height-6)}}
	if m.width < 80 || m.maximized {
		if m.detail || m.maximized {
			l.Detail = l.Body
		} else {
			l.List = l.Body
		}
	} else {
		left := max(26, m.width/3)
		l.List = rect{0, 3, left, l.Body.H}
		l.Detail = rect{left, 3, m.width - left, l.Body.H}
	}
	add := func(r rect, kind, id string, index int) {
		switch kind {
		case "row", "checkbox", "search-control", "run":
			r = intersect(r, l.List)
		case "preview", "hunk-action":
			r = intersect(r, l.Detail)
		}
		r.W = min(r.W, m.width-r.X)
		r.H = min(r.H, m.height-r.Y)
		if r.W > 0 && r.H > 0 {
			l.Hits = append(l.Hits, hitTarget{r, kind, id, index})
		}
	}
	if m.dialog != nil && m.dialog.kind != "filter" && m.dialog.kind != "search" {
		buttons := m.dialogButtons()
		if l.Body.H <= 1 {
			buttons = nil
		}
		x := 0
		for _, b := range buttons {
			label := "[" + b.label + "]"
			add(rect{x, l.Body.Y + l.Body.H - 1, ansi.StringWidth(label), 1}, "dialog", b.id, 0)
			x += ansi.StringWidth(label) + 2
		}
		if m.dialog.kind == "palette" {
			items := m.paletteActions()
			top := max(0, m.dialog.selected-max(1, l.Body.H-5)+1)
			for i := top; i < len(items); i++ {
				y := l.Body.Y + 2 + i - top
				if y >= l.Body.Y+l.Body.H-2 {
					break
				}
				add(rect{1, y, max(1, l.Body.W-2), 1}, "palette-row", items[i].id, i)
			}
		} else if m.dialog.kind == "records" {
			d := m.dialog
			prefix := len(wrap(singleLine(d.entry.Source), max(1, l.Body.W-4))) + 1
			if d.err != "" {
				prefix++
			}
			if d.loading {
				prefix++
			}
			top := max(0, d.selected-max(1, (l.Body.H-7)/3)+1)
			for i := top; i < len(d.records); i++ {
				y := l.Body.Y + 1 + prefix + (i-top)*3
				if y >= l.Body.Y+l.Body.H-2 {
					break
				}
				add(rect{1, y, max(1, l.Body.W-2), min(3, l.Body.Y+l.Body.H-2-y)}, "record", d.records[i].Bucket+"/"+d.records[i].Key, i)
			}
		}
		return l
	}
	if m.dialog == nil {
		x := 0
		for i, label := range m.tabLabels() {
			add(rect{x, 2, ansi.StringWidth(label), 1}, "tab", "", i)
			x += ansi.StringWidth(label) + 3
		}
	}
	if l.List.W > 0 {
		content := m.listContent(l.List.W, l.List.H)
		inset := 1
		if l.List.W < 3 || l.List.H < 3 {
			inset = 0
		}
		for _, row := range content.Rows {
			y := l.List.Y + inset + row.Line
			if y >= l.List.Y+l.List.H-inset {
				continue
			}
			if m.tab == 2 && y >= l.List.Y+l.List.H-2 {
				continue
			}
			if m.tab == 0 && m.dialog == nil {
				add(rect{l.List.X + inset + 1, y, 1, 1}, "checkbox", row.ID, row.Index)
			}
			add(rect{l.List.X + inset, y, max(1, l.List.W-2*inset), 1}, "row", row.ID, row.Index)
		}
		if m.tab == 3 && len(content.Lines) > 0 {
			for _, part := range []struct{ text, id string }{{"Source", "search-source"}, {"Current", "search-current"}, {"Regex", "search-regex"}} {
				idx := strings.Index(content.Lines[0], part.text)
				if idx >= 0 {
					add(rect{l.List.X + inset + idx, l.List.Y + inset, len(part.text), 1}, "search-control", part.id, 0)
				}
			}
		}
		if m.tab == 2 && m.dialog == nil {
			add(rect{l.List.X + 1, l.List.Y + l.List.H - 2, 5, 1}, "run", "", m.maintenanceSelected)
		}
		add(l.List, "pane", "list", 0)
	}
	if l.Detail.W > 0 {
		if m.tab < 2 && m.dialog == nil {
			labels := m.previewLabels()
			x := l.Detail.X + 3
			for i, label := range labels {
				add(rect{x, l.Detail.Y, ansi.StringWidth(label), 1}, "preview", "", i)
				x += ansi.StringWidth(label) + 3
			}
		}
		if m.tab == 0 && m.dialog == nil && m.lists[0].view == 3 && m.lists[0].hunkMode && hunkCount(&m.lists[0]) > 0 {
			for _, button := range m.hunkButtons() {
				add(rect{l.Detail.X + 1 + button.x, l.Detail.Y + 1 + button.line, ansi.StringWidth(button.label), 1}, "hunk-action", button.id, 0)
			}
		}
		add(l.Detail, "pane", "detail", 0)
	}
	if m.dialog == nil {
		footer := m.footer()
		y := l.Body.Y + l.Body.H + 1
		for _, a := range m.actions() {
			if !a.enabled || !a.footer {
				continue
			}
			hint := a.key + " " + footerLabel(a.id)
			if idx := strings.Index(footer, hint); idx >= 0 {
				add(rect{idx, y, ansi.StringWidth(hint), 1}, "action", a.id, 0)
			}
		}
		if idx := strings.Index(footer, "q quit"); idx >= 0 {
			add(rect{idx, y, 6, 1}, "quit", "", 0)
		}
	}
	return l
}

func (m *model) hit(x, y int) *hitTarget {
	for _, h := range m.layout().Hits {
		if h.Rect.contains(x, y) {
			copy := h
			return &copy
		}
	}
	return nil
}
func sameHit(a, b *hitTarget) bool { return a != nil && b != nil && *a == *b }

func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	if !m.opts.Mouse {
		return nil
	}
	event := msg.Mouse()
	hit := m.hit(event.X, event.Y)
	switch msg.(type) {
	case tea.MouseWheelMsg:
		m.pressed = nil
		delta := 3
		if event.Button == tea.MouseWheelUp {
			delta = -3
		} else if event.Button != tea.MouseWheelDown {
			return nil
		}
		if m.dialog != nil && m.dialog.kind != "filter" && m.dialog.kind != "search" {
			d := m.dialog
			switch d.kind {
			case "palette":
				d.selected = max(0, min(d.selected+delta, len(m.paletteActions())-1))
			case "records":
				d.selected = max(0, min(d.selected+delta, len(d.records)-1))
			default:
				d.selected = max(0, d.selected+delta)
			}
			return nil
		}
		l := m.layout()
		old := m.detail
		if l.List.contains(event.X, event.Y) {
			m.detail = false
		} else if l.Detail.contains(event.X, event.Y) {
			m.detail = true
		} else {
			return nil
		}
		cmd := m.move(delta)
		m.detail = old
		return cmd
	case tea.MouseClickMsg:
		if event.Button == tea.MouseRight {
			if m.dialog == nil {
				var cmd tea.Cmd
				if hit != nil && (hit.Kind == "row" || hit.Kind == "checkbox") {
					row := *hit
					row.Kind = "row"
					cmd = m.activateHit(row)
				}
				return tea.Batch(cmd, m.openInput("palette"))
			}
			return nil
		}
		if event.Button == tea.MouseLeft {
			m.pressed = hit
		}
		return nil
	case tea.MouseMotionMsg:
		if m.pressed != nil && !sameHit(m.pressed, hit) {
			m.pressed = nil
		}
		return nil
	case tea.MouseReleaseMsg:
		pressed := m.pressed
		m.pressed = nil
		if (event.Button != tea.MouseLeft && event.Button != tea.MouseNone) || !sameHit(pressed, hit) {
			return nil
		}
		return m.activateHit(*hit)
	}
	return nil
}

func (m *model) activateHit(h hitTarget) tea.Cmd {
	if m.dialog != nil && m.dialog.kind != "filter" && m.dialog.kind != "search" {
		switch h.Kind {
		case "dialog":
			if h.ID == "cancel" {
				m.closeDialog()
				return nil
			}
			return m.dialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "palette-row":
			m.dialog.selected = h.Index
		case "record":
			m.dialog.selected = h.Index
			m.dialog.checked[h.Index] = !m.dialog.checked[h.Index]
		}
		return nil
	}
	switch h.Kind {
	case "tab":
		return m.switchTab(h.Index)
	case "row":
		m.detail = false
		if m.tab == 2 {
			m.maintenanceSelected = h.Index
			return nil
		}
		if m.tab == 3 {
			m.search.selected = h.Index
			return m.loadSearchPreview()
		}
		m.lists[m.tab].selected = h.Index
		return m.loadPreview()
	case "checkbox":
		m.lists[0].checked[h.ID] = !m.lists[0].checked[h.ID]
		return nil
	case "pane":
		if m.dialog != nil {
			return nil
		}
		m.detail = h.ID == "detail"
		return nil
	case "preview":
		m.lists[m.tab].view = h.Index
		m.lists[m.tab].scroll = 0
		return m.loadPreview()
	case "action":
		return m.dispatch(h.ID)
	case "hunk-action":
		return m.dispatch(h.ID)
	case "run":
		return m.dispatch(maintenanceActions[h.Index])
	case "quit":
		return tea.Quit
	case "search-control":
		if h.ID == "search-regex" {
			m.search.regex = !m.search.regex
		} else {
			m.search.scope = strings.TrimPrefix(h.ID, "search-")
		}
		return m.scheduleSearch()
	}
	return nil
}

type dialogButton struct{ id, label string }

func (m *model) dialogButtons() []dialogButton {
	switch m.dialog.kind {
	case "confirm":
		return []dialogButton{{"confirm", "Confirm"}, {"cancel", "Cancel"}}
	case "records":
		return []dialogButton{{"review", "Review"}, {"cancel", "Cancel"}}
	case "palette":
		return []dialogButton{{"run", "Run"}, {"cancel", "Cancel"}}
	default:
		return []dialogButton{{"cancel", "Close"}}
	}
}

func (m *model) listPane(width, height int) []string {
	content := m.listContent(width, height)
	if m.tab == 2 {
		available := max(1, height-2)
		for len(content.Lines) < available {
			content.Lines = append(content.Lines, "")
		}
		content.Lines[available-1] = "[Run] · selected action"
	}
	return m.pane(content.Title, content.Lines, width, height, !m.detail)
}

func (m *model) searchListContent(width, height int) paneContent {
	s := &m.search
	source, current := "Source", "Current"
	if s.scope == "source" {
		source = "[Source]"
	} else {
		current = "[Current]"
	}
	regex := "Regex:off"
	if s.regex {
		regex = "Regex:on"
	}
	lines := []string{source + " " + current + " " + regex, "query: " + singleLine(s.text)}
	if s.stale {
		lines = append(lines, "Previous results · stale")
	}
	if s.resultQuery.Text != "" && (s.resultQuery.Text != s.text || s.resultQuery.Scope != s.scope || s.resultQuery.Regex != s.regex) {
		lines = append(lines, "Results for: "+singleLine(s.resultQuery.Text)+" ("+s.resultQuery.Scope+")")
	}
	if s.loading {
		lines = append(lines, "Searching…")
	}
	if s.err != "" {
		lines = append(lines, errorText(s.err))
	}
	if s.result.Truncated {
		lines = append(lines, "Results limited; refine query")
	}
	if s.result.Skipped > 0 {
		lines = append(lines, fmt.Sprintf("%d unreadable/binary files skipped", s.result.Skipped))
	}
	if len(s.result.Errors) > 0 {
		lines = append(lines, fmt.Sprintf("%d search warnings · see preview", len(s.result.Errors)))
	}
	var rows []rowHit
	available := max(1, height-2-len(lines))
	top := max(0, s.selected-available+1)
	for i := top; i < len(s.result.Matches) && i < top+available; i++ {
		match := s.result.Matches[i]
		rows = append(rows, rowHit{len(lines), i, matchKey(match)})
		marker := "  "
		if i == s.selected {
			marker = "> "
		}
		line := fmt.Sprintf("%s%s:%d:%d  %s", marker, singleLine(match.Relative), match.Line, match.Column, singleLine(match.Text))
		if i == s.selected {
			line = m.paint(line, "6")
		}
		lines = append(lines, line)
	}
	if len(s.result.Matches) == 0 && !s.loading {
		label := "s: search Source / Current contents"
		if s.text != "" {
			label = "No matches"
		}
		lines = append(lines, label)
	}
	return paneContent{fmt.Sprintf("Search · %d matches", len(s.result.Matches)), lines, rows}
}

func (m *model) previewLabels() []string {
	labels := []string{"Source", "Current", "Rendered", "Diff"}
	if m.tab < 2 {
		labels[m.lists[m.tab].view] = "[" + labels[m.lists[m.tab].view] + "]"
	}
	return labels
}
func footerLabel(id string) string {
	labels := map[string]string{"edit": "edit", "search-edit": "edit source", "apply": "apply", "script-apply": "apply script", "fetch": "fetch", "update": "update", "lazygit": "lazygit", "palette": "actions", "help": "help", "search": "search", "hunks": "hunks"}
	return labels[id]
}

type hunkButton struct {
	id, label string
	x, line   int
}

func (m *model) hunkButtons() []hunkButton {
	lines := m.hunkControlLines()
	var result []hunkButton
	for _, b := range []hunkButton{{id: "prev-hunk", label: "[Prev]", line: 4}, {id: "next-hunk", label: "[Next]", line: 4}, {id: "hunk-to-current", label: "[< Source → Current]", line: 5}, {id: "hunk-to-source", label: "[> Current → Source]", line: 5}} {
		line := lines[b.line-4]
		idx := strings.Index(line, b.label)
		b.x = ansi.StringWidth(line[:idx])
		result = append(result, b)
	}
	return result
}

func (m *model) hunkControlLines() []string {
	s := &m.lists[0]
	return []string{fmt.Sprintf("Hunk %d/%d [Prev] [Next] · n/N", s.hunkIndex+1, hunkCount(s)), "[< Source → Current] [> Current → Source]"}
}
