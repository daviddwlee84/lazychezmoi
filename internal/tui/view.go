package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) paint(s, color string) string {
	if !m.opts.Color {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(s)
}

func fit(s string, width int) string {
	width = max(0, width)
	s = ansi.Truncate(s, width, "")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

func (m *model) View() tea.View {
	w, h := max(1, m.width), max(1, m.height)
	rows := []string{m.header(), "destination: " + singleLine(m.scope.DestinationDir)}
	if m.scopeErr != "" {
		rows[1] = m.paint(errorText(m.scopeErr), "1")
	}
	tabs := m.tabLabels()
	for i := range tabs {
		if i == m.tab {
			tabs[i] = m.paint(tabs[i], "6")
		}
	}
	rows = append(rows, strings.Join(tabs, "   ")+"   Tab: focus  v: preview")
	layout := m.layout()
	bodyHeight := layout.Body.H
	if m.dialog != nil && m.dialog.kind != "filter" && m.dialog.kind != "search" {
		rows = append(rows, m.dialogView(w, bodyHeight)...)
	} else {
		rows = append(rows, m.body(w, bodyHeight)...)
	}
	status := singleLine(m.status)
	if m.pending != nil {
		status = "BUSY · " + status
	}
	if m.prefix {
		status = "g… (g: first row) · " + status
	}
	if m.statusErr {
		status = m.paint("ERROR · "+status, "1")
	}
	rows = append(rows, status, m.footer())
	if m.dialog != nil && (m.dialog.kind == "filter" || m.dialog.kind == "search") {
		rows = append(rows, m.inputView()+"  Enter: accept · Esc: clear")
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	if len(rows) > h {
		rows = rows[:h]
	}
	for i := range rows {
		rows[i] = fit(rows[i], w)
	}
	v := tea.NewView(strings.Join(rows, "\n"))
	v.AltScreen = true
	if m.opts.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m *model) header() string {
	source := m.scope.SourceDir
	if source == "" {
		source = "discovering…"
	}
	git := "Git…"
	if m.gitLoaded {
		git = ansi.Truncate(singleLine(m.git.Branch), 20, "…")
		if m.git.Upstream == "" {
			git += " · no upstream"
		} else {
			git += fmt.Sprintf(" ↑%d ↓%d", m.git.Ahead, m.git.Behind)
		}
		if m.git.Dirty {
			git += " *"
		}
		if m.gitStale {
			git += " stale"
		}
		if !m.git.FetchedAt.IsZero() && !m.fetching {
			git += " fetched:" + m.git.FetchedAt.Local().Format("15:04")
		}
	}
	if m.fetching {
		git += " fetching…"
	}
	if m.gitErr != "" {
		git = "Git unavailable · : results"
	}
	prefix := "lazychezmoi"
	if m.width < 60 {
		return prefix + " · " + git
	}
	remaining := max(0, m.width-ansi.StringWidth(prefix+"  source:   "+git))
	path := singleLine(source)
	if ansi.StringWidth(path) > remaining {
		path = ansi.TruncateLeft(path, ansi.StringWidth(path)-remaining+1, "…")
	}
	path = ansi.Truncate(path, remaining, "")
	left := prefix + "  source: " + path
	return fit(left, max(0, m.width-ansi.StringWidth(git)-1)) + " " + git
}

func (m *model) body(width, height int) []string {
	l := m.layout()
	if l.List.W == 0 || l.Detail.W == 0 {
		if l.Detail.W > 0 {
			return m.detailPane(width, height)
		}
		return m.listPane(width, height)
	}
	left := l.List.W
	right := l.Detail.W
	lrows, rrows := m.listPane(left, height), m.detailPane(right, height)
	result := make([]string, height)
	for i := range result {
		result[i] = lrows[i] + rrows[i]
	}
	return result
}

func (m *model) pane(title string, lines []string, width, height int, focused bool) []string {
	if width < 3 || height < 3 {
		result := make([]string, height)
		for i := range result {
			if i < len(lines) {
				result[i] = fit(lines[i], width)
			} else {
				result[i] = fit("", width)
			}
		}
		return result
	}
	inner := width - 2
	mark := " "
	if focused {
		mark = ">"
	}
	top := "┌" + fit(mark+" "+title+" ", inner) + "┐"
	if focused {
		top = m.paint(top, "6")
	}
	result := []string{top}
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		result = append(result, "│"+fit(line, inner)+"│")
	}
	result = append(result, "└"+strings.Repeat("─", inner)+"┘")
	return result
}

func (m *model) listContent(width, height int) paneContent {
	if m.tab == 3 {
		return m.searchListContent(width, height)
	}
	if m.tab == 2 {
		var lines []string
		for i, id := range maintenanceActions {
			label := id
			enabled := true
			for _, a := range m.actions() {
				if a.id == id {
					label = a.label
					enabled = a.enabled
					break
				}
			}
			marker := "  "
			if i == m.maintenanceSelected {
				marker = "> "
			}
			if !enabled {
				label += " (busy)"
			}
			line := marker + label
			if i == m.maintenanceSelected {
				line = m.paint(line, "6")
			}
			lines = append(lines, line)
		}
		top := max(0, m.maintenanceSelected-max(1, height-2)+1)
		var hits []rowHit
		for i := top; i < len(maintenanceActions); i++ {
			hits = append(hits, rowHit{Line: i - top, Index: i, ID: maintenanceActions[i]})
		}
		return paneContent{Title: "Maintenance", Lines: lines[min(top, len(lines)):], Rows: hits}
	}
	s := &m.lists[m.tab]
	entries := s.visible()
	title := fmt.Sprintf("Files · %d", len(entries))
	if m.tab == 1 {
		title = fmt.Sprintf("Scripts · %d", len(entries))
	}
	if s.loading {
		title += " · loading"
	}
	if s.stale {
		title += " · stale"
	}
	var lines []string
	var hits []rowHit
	if s.query != "" {
		lines = append(lines, "filter: "+singleLine(s.query))
	}
	if s.changed {
		lines = append(lines, "changed only · c: all")
	}
	selectedCount := 0
	for _, checked := range s.checked {
		if checked {
			selectedCount++
		}
	}
	if selectedCount > 0 {
		lines = append(lines, fmt.Sprintf("%d selected (including hidden)", selectedCount))
	}
	if s.err != "" {
		lines = append(lines, m.paint(errorText(s.err), "1"))
	}
	if len(entries) == 0 {
		text := "No managed files. : actions / r retry"
		if m.tab == 1 {
			text = "No managed scripts"
		}
		if s.loading && !s.loaded {
			text = "Loading… navigation remains available"
		} else if s.query != "" || s.changed {
			text = "No matching targets · / filter · c all"
		}
		lines = append(lines, text)
	} else {
		available := max(1, height-2-len(lines))
		top := max(0, s.selected-available+1)
		for i := top; i < len(entries) && i < top+available; i++ {
			e := entries[i]
			marker := " "
			if i == s.selected {
				marker = ">"
			}
			check := " "
			if s.checked[e.ID] {
				check = "*"
			}
			state := "  "
			if entryChanged(e) {
				state = "Δ "
			} else if entryUnknown(e) {
				state = "? "
			}
			label := singleLine(entryLabel(e))
			if e.Template {
				label += " [tmpl]"
			}
			if e.Encrypted {
				label += " [encrypted]"
			}
			if e.Kind == "create" {
				label += " [create only]"
			}
			hits = append(hits, rowHit{Line: len(lines), Index: i, ID: e.ID})
			line := marker + check + state + label
			if i == s.selected {
				line = m.paint(line, "6")
			}
			lines = append(lines, line)
		}
	}
	return paneContent{Title: title, Lines: lines, Rows: hits}
}

func (m *model) detailPane(width, height int) []string {
	if m.tab == 3 {
		return m.searchDetailPane(width, height)
	}
	if m.tab == 2 {
		text := maintenanceDescription(maintenanceActions[m.maintenanceSelected])
		lines := wrap(text, max(1, width-4))
		lines = append(lines, "", "Enter: run selected action", "")
		for _, value := range []string{"Source: " + singleLine(m.scope.SourceDir), "Source state: " + singleLine(m.scope.SourceStateDir), "Destination: " + singleLine(m.scope.DestinationDir), "Config: " + singleLine(m.scope.ConfigFile), "chezmoi: " + singleLine(m.scope.Version)} {
			lines = append(lines, wrap(value, max(1, width-4))...)
		}
		if m.gitErr != "" {
			lines = append(lines, "", errorText(m.gitErr))
		}
		return m.pane("Details", lines, width, height, m.detail)
	}
	s := &m.lists[m.tab]
	e, ok := m.selectedEntry()
	labels := m.previewLabels()
	title := strings.Join(labels, " · ")
	var lines []string
	if !ok {
		lines = []string{"Select a target to preview it"}
	} else {
		lines = append(lines, singleLine(entryLabel(e)))
		flags := singleLine(e.Kind)
		if e.Drift == "?" {
			flags += " · local: unknown"
		} else if strings.TrimSpace(e.Drift) != "" {
			flags += " · local changed: " + singleLine(e.Drift)
		}
		if e.Pending == "?" {
			flags += " · apply: unknown"
		} else if strings.TrimSpace(e.Pending) != "" {
			flags += " · apply changes: " + singleLine(e.Pending)
		}
		if e.Kind == "create" {
			flags += " · only creates absent files"
		}
		if e.Encrypted && s.view == 0 {
			flags += " · encrypted source (ciphertext)"
		}
		lines = append(lines, flags)
		if s.view == 3 {
			after := "Rendered"
			if s.snapshot != nil {
				after = "Source"
			}
			labels := "Before: Current → After: " + after
			if s.renderLayout == "side-by-side" {
				labels = fit("Before: Current", max(1, (width-2)/2)) + "After: " + after
			}
			lines = append(lines, labels, "Renderer: "+singleLine(s.renderName)+" · "+singleLine(s.renderLayout))
			if s.hunkMode {
				if hunkCount(s) > 0 {
					lines = append(lines, m.hunkControlLines()...)
					lines = append(lines, singleLine(s.snapshot.Hunks[s.hunkIndex].Header))
				} else if s.hunkReason != "" {
					lines = append(lines, "Copy unavailable: "+singleLine(s.hunkReason))
				} else {
					lines = append(lines, "No text hunks to copy")
				}
			}
			if s.snapshot != nil {
				for _, note := range s.snapshot.Notes {
					lines = append(lines, singleLine(note))
				}
			}
			if s.renderWarning != "" {
				lines = append(lines, singleLine(s.renderWarning))
			}
		}
		if s.previewLoading {
			lines = append(lines, "Loading preview…")
		}
		if s.previewErr != "" {
			lines = append(lines, m.paint(errorText(s.previewErr), "1"))
		}
		if s.stale {
			lines = append(lines, "Stale snapshot; refresh pending")
		}
		content := strings.Split(s.preview, "\n")
		if s.preview == "" && !s.previewLoading && s.previewErr == "" {
			content = []string{"(empty)"}
		}
		top := min(s.scroll, max(0, len(content)-1))
		lines = append(lines, content[top:]...)
	}
	return m.pane(title, lines, width, height, m.detail)
}

func (m *model) footer() string {
	if m.dialog != nil {
		switch m.dialog.kind {
		case "search":
			return "Typing searches · ↑↓ select · Enter accepts · Esc leaves input"
		case "filter":
			return "↑↓ select · text filters · Enter accept · Esc clear"
		case "palette":
			return "Type to search actions · ↑↓ select · Enter run · Esc back"
		case "records":
			return "↑↓ / jk select · Space toggle record · Enter review · Esc cancel"
		case "confirm":
			return "↑↓ scroll review · Enter confirm · Esc cancel"
		default:
			return "↑↓ / jk scroll · Esc back"
		}
	}
	var hints []string
	for _, a := range m.actions() {
		if a.footer && a.enabled {
			label := footerLabel(a.id)
			hints = append(hints, a.key+" "+label)
		}
	}
	if m.width < 22 {
		return "q quit"
	}
	if m.width < 40 {
		return ": actions  ? help  q quit"
	}
	var visible []string
	for _, hint := range hints {
		if ansi.StringWidth(strings.Join(append(append([]string{}, visible...), hint, "q quit"), "  ")) <= m.width {
			visible = append(visible, hint)
		}
	}
	visible = append(visible, "q quit")
	return strings.Join(visible, "  ")
}

func (m *model) dialogView(width, height int) []string {
	originalHeight := height
	height = max(1, height-1)
	d := m.dialog
	title := ""
	var lines []string
	switch d.kind {
	case "palette":
		title = "Actions"
		lines = append(lines, m.inputView())
		items := m.paletteActions()
		top := max(0, d.selected-max(1, height-4)+1)
		if len(items) == 0 {
			lines = append(lines, "No matching actions")
		}
		for i := top; i < len(items); i++ {
			a := items[i]
			prefix := "  "
			if i == d.selected {
				prefix = "> "
			}
			label := prefix + a.label
			if a.key != "" {
				label += "  (" + a.key + ")"
			}
			if i == d.selected {
				label = m.paint(label, "6")
			}
			lines = append(lines, label)
		}
	case "help":
		title = "Help · current context"
		lines = []string{"↑↓ / j k  Select / scroll     h l / ←→  Focus pane", "Tab / Shift+Tab  Focus pane   1 / 2 / 3  Switch view", "gg / Home  First   G / End  Last   PgUp/PgDn  Page", "/ filters; Enter accepts the filter without opening a target", "Space marks files; apply includes marked files hidden by a filter", "Δ means local drift or pending apply; Git state is separate", "v / [ / ] changes Source, Current, Rendered and Diff", "Rendered / Diff may execute template helpers or access externals", "A applies all, including scripts; a on Files excludes scripts", "q / Ctrl+C exits; Esc closes the nearest interaction", "", "Available actions:"}
		lines = append([]string{"m toggles mouse capture; disable it for terminal text selection", "Mouse: click selects; wheel scrolls hovered pane; right click opens actions", "4 / s: content search · Source / Current · literal or regex", "H: hunk picker · n/N: next/previous · < Source→Current · > Current→Source", "U: undo last hunk copy · z: maximize preview"}, lines...)
		for _, a := range m.actions() {
			if a.enabled {
				key := a.key
				if key == "" {
					key = ":"
				}
				lines = append(lines, fmt.Sprintf("%-8s %s", key, a.label))
			}
		}
	case "results":
		title = "Recent results (this session)"
		for _, result := range m.results {
			lines = append(lines, wrap(sanitize(result), max(1, width-4))...)
		}
		if len(lines) == 0 {
			lines = []string{"No operations have completed in this session"}
		}
	case "context":
		title = "Active context"
		for _, value := range []string{"Source: " + singleLine(m.scope.SourceDir), "Source state: " + singleLine(m.scope.SourceStateDir), "Working tree: " + singleLine(m.scope.WorkingTree), "Destination: " + singleLine(m.scope.DestinationDir), "Config: " + singleLine(m.scope.ConfigFile), "chezmoi: " + singleLine(m.scope.Version), "Git branch: " + singleLine(m.git.Branch), "Git upstream: " + singleLine(m.git.Upstream)} {
			lines = append(lines, wrap(value, max(1, width-4))...)
			lines = append(lines, "")
		}
		if m.scopeErr != "" {
			lines = append(lines, wrap(errorText(m.scopeErr), max(1, width-4))...)
		}
	case "records":
		title = "Choose exact script state records"
		lines = append(lines, wrap(singleLine(d.entry.Source), max(1, width-4))...)
		lines = append(lines, "run_once content hashes can be shared by scripts with identical content.")
		if d.loading {
			lines = append(lines, "Loading records…")
		}
		if d.err != "" {
			lines = append(lines, m.paint(errorText(d.err), "1"))
		}
		if !d.loading && d.err == "" && len(d.records) == 0 {
			lines = append(lines, "No recorded state to reset. Esc returns to the script.")
		}
		top := max(0, d.selected-max(1, (height-6)/3)+1)
		for i := top; i < len(d.records); i++ {
			r := d.records[i]
			prefix := "  [ ] "
			if d.checked[i] {
				prefix = "  [x] "
			}
			if i == d.selected {
				prefix = ">" + prefix[1:]
			}
			lines = append(lines, prefix+singleLine(r.Bucket), "      "+singleLine(r.Key), "      "+singleLine(r.Description))
		}
	case "confirm":
		op := d.confirm
		title = "Review · " + op.label
		if op.snapshot != nil {
			target := op.entry.Target
			if op.direction == "current-to-source" {
				target = op.entry.Source
			}
			lines = append(lines, wrap("Write: "+singleLine(target), max(1, width-4))...)
			lines = append(lines, "Only this text hunk will be copied; scripts are not run.", "Patch orientation: Current (-) → Source (+)")
			for _, h := range op.snapshot.Hunks {
				if h.ID == op.hunkID {
					lines = append(lines, wrap(sanitize(h.Patch), max(1, width-4))...)
					break
				}
			}
		} else if op.reset {
			lines = append(lines, wrap("Script: "+singleLine(op.entry.Source), max(1, width-4))...)
			lines = append(lines, "Remove only these persistent-state records:")
			for _, r := range op.records {
				lines = append(lines, wrap(singleLine(r.Bucket+" / "+r.Key), max(1, width-4))...)
			}
			lines = append(lines, "Identical run_once content may share these records.")
			if op.applyAfterReset {
				lines = append(lines, "After resetting, execute this script through chezmoi.")
			} else {
				lines = append(lines, "The script will be eligible to run on a later apply.")
			}
		} else {
			lines = append(lines, wrap("Targets: "+sanitize(targetsDescription(op.op.Targets)), max(1, width-4))...)
			if op.op.Kind == "re-add" {
				lines = append(lines, "Replace source contents with the current local file.")
			} else {
				lines = append(lines, "Includes scripts and their configured hooks.")
			}
		}
		lines = append(lines, "", "Enter performs this action. Esc leaves state unchanged.")
	}
	if d.kind == "help" || d.kind == "results" || d.kind == "context" || d.kind == "confirm" {
		top := min(d.selected, max(0, len(lines)-max(1, height-2)))
		lines = lines[top:]
	}
	rows := m.pane(title, lines, width, height, true)
	var buttons []string
	for _, b := range m.dialogButtons() {
		buttons = append(buttons, "["+b.label+"]")
	}
	if originalHeight > 1 {
		rows = append(rows, fit(strings.Join(buttons, "  "), width))
	}
	return rows
}

func (m *model) searchDetailPane(width, height int) []string {
	s := &m.search
	var lines []string
	match := m.selectedMatch()
	if match == nil {
		lines = []string{"s: type a keyword to search file contents", "Source searches the repository; Current searches managed live files."}
	} else {
		lines = append(lines, fmt.Sprintf("%s:%d:%d", singleLine(match.Path), match.Line, match.Column), "Scope: "+match.Scope+" · e: edit source")
		if match.Entry == nil {
			lines = append(lines, "Repository helper · no apply action")
		}
		if s.previewLoading {
			lines = append(lines, "Loading preview…")
		}
		if s.previewErr != "" {
			lines = append(lines, errorText(s.previewErr))
		}
		if s.previewWarning != "" {
			lines = append(lines, s.previewWarning)
		}
		body := strings.Split(s.preview, "\n")
		top := min(s.scroll, max(0, len(body)-1))
		for i := top; i < len(body); i++ {
			marker := " "
			if i+1 == match.Line {
				marker = ">"
			}
			line := fmt.Sprintf("%s%4d %s", marker, i+1, body[i])
			if i+1 == match.Line {
				line = m.paint(line, "3")
			}
			lines = append(lines, line)
		}
	}
	if len(s.result.Errors) > 0 {
		for _, err := range s.result.Errors {
			lines = append(lines, errorText(err))
		}
	}
	return m.pane("Search preview", lines, width, height, m.detail)
}

func wrap(s string, width int) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		for ansi.StringWidth(line) > width && width > 0 {
			cut := ansi.Truncate(line, width, "")
			if cut == "" {
				break
			}
			result = append(result, cut)
			line = strings.TrimPrefix(line, cut)
		}
		result = append(result, line)
	}
	return result
}

func (m *model) inputView() string {
	text := m.dialog.input.View()
	if !m.opts.Color {
		return ansi.Strip(text)
	}
	return text
}

func maintenanceDescription(id string) string {
	switch id {
	case "init":
		return "Run native chezmoi init. New template parameters can prompt in the terminal."
	case "init-prompt":
		return "Run native chezmoi init --prompt to ask for parameter values again."
	case "edit-config":
		return "Open the active chezmoi configuration in your configured editor."
	case "edit-config-template":
		return "Open the source configuration template through chezmoi."
	case "source":
		return "Open the resolved source tree in your configured editor. Close the editor to return to this dashboard."
	case "externals":
		return "Refresh and apply external resources through chezmoi, excluding scripts. This can access the network and update managed files."
	case "update-reload":
		return "Update with init, then exit and ask the installed parent shell wrapper to reload. Failure returns here without reloading."
	case "reload":
		return "Exit and ask the installed shell wrapper to reload. PowerShell needs dot-source invocation to affect caller scope."
	case "results":
		return "Review recent operation outcomes retained for this dashboard session."
	case "context":
		return "Inspect complete source, working tree, destination and configuration paths for this invocation."
	}
	return ""
}
