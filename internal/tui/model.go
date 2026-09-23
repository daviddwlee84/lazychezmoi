// Package tui provides the interactive chezmoi workbench.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
	"github.com/daviddwlee84/lazychezmoi/internal/diffview"
	"github.com/daviddwlee84/lazychezmoi/internal/search"
	"github.com/daviddwlee84/lazychezmoi/internal/shell"
)

// Options controls recurring dashboard preferences.
type Options struct {
	AutoFetch, Color, Mouse bool
	Diff                    diffview.Options
	Search                  *search.Service
}

type backend interface {
	Invalidate()
	Resolve(context.Context) (chezmoi.Context, error)
	Inventory(context.Context, bool) ([]chezmoi.Entry, error)
	Status(context.Context, bool) (map[string]chezmoi.EntryStatus, error)
	GitStatus(context.Context) (chezmoi.GitStatus, error)
	Fetch(context.Context, bool) error
	Preview(context.Context, chezmoi.Entry, string) (string, error)
	Command(context.Context, chezmoi.Operation) (*exec.Cmd, error)
	ScriptRecords(context.Context, chezmoi.Entry) ([]chezmoi.ScriptRecord, error)
	ResetScript(context.Context, chezmoi.Entry, []chezmoi.ScriptRecord) error
	ScriptResult(context.Context, chezmoi.Entry, time.Time) (string, error)
}

// Run opens the dashboard. Construction does no filesystem or subprocess work.
func Run(ctx context.Context, service *chezmoi.Service, opts Options) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	m := newModel(ctx, service, opts)
	result, err := newProgram(ctx, m).Run()
	if err != nil {
		return err
	}
	if result.(*model).reload {
		return shell.Request()
	}
	return nil
}

// The caller's context owns signal cancellation. Tea 2.0.9's separate signal
// goroutine can block sending QuitMsg after that context stops the event loop,
// which deadlocks terminal restoration while shutdown waits for the goroutine.
func newProgram(ctx context.Context, m tea.Model, extra ...tea.ProgramOption) *tea.Program {
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	return tea.NewProgram(m, append(opts, extra...)...)
}

type listState struct {
	entries                                 []chezmoi.Entry
	query                                   string
	changed                                 bool
	selected, top                           int
	checked                                 map[string]bool
	loaded, loading, stale                  bool
	err                                     string
	gen                                     uint64
	cancel                                  context.CancelFunc
	statusKnown, statusLoading, statusStale bool
	statusErr                               string
	statusGen                               uint64
	statusCancel                            context.CancelFunc
	sortPending                             bool
	view                                    int
	preview                                 string
	previewID                               string
	previewLoading                          bool
	previewErr                              string
	previewGen                              uint64
	previewCancel                           context.CancelFunc
	scroll                                  int
	rawDiff                                 string
	snapshot                                *chezmoi.DiffSnapshot
	hunkReason                              string
	hunkMode                                bool
	hunkIndex                               int
	renderGen                               uint64
	renderCancel                            context.CancelFunc
	renderWarning                           string
	renderLayout, renderName                string
}

type pendingOperation struct {
	id                             uint64
	label                          string
	op                             chezmoi.Operation
	entry                          chezmoi.Entry
	records                        []chezmoi.ScriptRecord
	reset, applyAfterReset, reload bool
	started                        time.Time
	resetDone                      bool
	snapshot                       *chezmoi.DiffSnapshot
	hunkID, direction, searchPath  string
	undo                           *chezmoi.CopyReceipt
}

type dialog struct {
	kind            string
	input           textinput.Model
	selected        int
	gen             uint64
	cancel          context.CancelFunc
	entry           chezmoi.Entry
	records         []chezmoi.ScriptRecord
	checked         map[int]bool
	applyAfterReset bool
	loading         bool
	err             string
	confirm         *pendingOperation
}

type model struct {
	ctx                 context.Context
	service             backend
	opts                Options
	width, height       int
	tab                 int
	detail              bool
	lists               [2]listState
	maintenanceSelected int
	scope               chezmoi.Context
	scopeErr            string
	scopeGen            uint64
	git                 chezmoi.GitStatus
	gitLoaded           bool
	gitStale            bool
	gitErr              string
	gitGen              uint64
	fetching            bool
	fetchGen            uint64
	fetchCancel         context.CancelFunc
	autoFetchStarted    bool
	refreshGen          uint64
	invalidations       invalidationGate
	dialog              *dialog
	dialogGen           uint64
	pending             *pendingOperation
	operationGen        uint64
	status              string
	statusErr           bool
	results             []string
	prefix              bool
	prefixGen           uint64
	reload              bool
	owner               *chezmoi.Service
	searcher            searchBackend
	search              searchState
	maximized           bool
	pressed             *hitTarget
	lastCopy            *chezmoi.CopyReceipt
	renderer            func(context.Context, string, diffview.Options) (diffview.Result, error)
	backgroundDark      bool
}

type scopeMsg struct {
	gen   uint64
	scope chezmoi.Context
	err   error
}
type entriesMsg struct {
	tab     int
	gen     uint64
	entries []chezmoi.Entry
	err     error
}
type previewMsg struct {
	tab               int
	gen               uint64
	id, view, content string
	err               error
	snapshot          *chezmoi.DiffSnapshot
	hunkReason        string
}
type gitMsg struct {
	gen uint64
	git chezmoi.GitStatus
	err error
}
type fetchedMsg struct {
	gen uint64
	err error
}
type preparedMsg struct {
	id  uint64
	cmd *exec.Cmd
	err error
}
type operationMsg struct {
	id  uint64
	err error
}
type operationCompleteMsg struct {
	id  uint64
	err error
}
type resetMsg struct {
	id  uint64
	err error
}
type recordsMsg struct {
	gen     uint64
	records []chezmoi.ScriptRecord
	err     error
}
type scriptResultMsg struct {
	id                uint64
	label, result     string
	err, operationErr error
}
type prefixExpiredMsg struct{ gen uint64 }
type reloadCheckedMsg struct {
	gen       uint64
	operation *pendingOperation
	err       error
}

var previewViews = []string{"source", "current", "rendered", "diff"}

func newModel(ctx context.Context, service backend, opts Options) *model {
	m := &model{ctx: ctx, service: service, opts: opts, width: 100, height: 28, status: "Loading local source…"}
	m.owner, _ = service.(*chezmoi.Service)
	m.searcher = opts.Search
	if opts.Search == nil {
		m.searcher = search.New(search.Options{})
	}
	m.search.scope = "source"
	m.renderer = diffview.Render
	m.backgroundDark = true
	for i := range m.lists {
		m.lists[i].checked = make(map[string]bool)
	}
	return m
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadScope(), m.loadEntries(0), m.loadGit()}
	if m.opts.Diff.Theme == "" || m.opts.Diff.Theme == "auto" {
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	return tea.Batch(cmds...)
}

func (m *model) loadScope() tea.Cmd {
	m.scopeGen++
	gen, service, ctx := m.scopeGen, m.service, m.ctx
	return func() tea.Msg { c, e := service.Resolve(ctx); return scopeMsg{gen, c, e} }
}

func (m *model) loadEntries(tab int) tea.Cmd {
	s := &m.lists[tab]
	if s.cancel != nil {
		s.cancel()
	}
	if s.statusCancel != nil {
		s.statusCancel()
	}
	s.statusGen++
	s.statusLoading = false
	s.statusStale = s.statusKnown
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	s.gen++
	s.loading = true
	gen, service := s.gen, m.service
	return func() tea.Msg {
		defer cancel()
		e, err := service.Inventory(ctx, tab == 1)
		return entriesMsg{tab, gen, e, err}
	}
}

func (m *model) loadGit() tea.Cmd {
	m.gitGen++
	gen, service, ctx := m.gitGen, m.service, m.ctx
	return func() tea.Msg { g, e := service.GitStatus(ctx); return gitMsg{gen, g, e} }
}

func (m *model) fetch() tea.Cmd {
	if m.fetching || m.pending != nil {
		return nil
	}
	m.fetching = true
	m.fetchGen++
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	m.fetchCancel = cancel
	gen, service := m.fetchGen, m.service
	return func() tea.Msg { defer cancel(); return fetchedMsg{gen, service.Fetch(ctx, true)} }
}

func (m *model) loadPreview() tea.Cmd {
	if m.tab > 1 {
		return nil
	}
	tab := m.tab
	s := &m.lists[tab]
	if s.previewCancel != nil {
		s.previewCancel()
	}
	s.previewGen++
	e, ok := m.selectedEntry()
	if !ok {
		s.preview = ""
		s.previewID = ""
		s.previewLoading = false
		s.previewErr = ""
		return nil
	}
	if s.previewID != e.ID {
		s.preview = ""
		s.scroll = 0
		s.rawDiff = ""
		s.snapshot = nil
		s.hunkReason = ""
		s.hunkIndex = 0
		s.renderGen++
	}
	s.previewID = e.ID
	if m.pending != nil {
		s.previewLoading = false
		return nil
	}
	s.previewLoading = true
	s.previewErr = ""
	ctx, cancel := context.WithCancel(m.ctx)
	s.previewCancel = cancel
	gen, view, service, color := s.previewGen, previewViews[s.view], m.service, m.opts.Color
	return func() tea.Msg {
		defer cancel()
		if view == "diff" {
			return m.readDiff(ctx, tab, gen, e)
		}
		content, err := service.Preview(ctx, e, view)
		if err == nil {
			content = highlight(content, e, view, color)
		}
		return previewMsg{tab: tab, gen: gen, id: e.ID, view: view, content: content, err: err}
	}
}

func (s *listState) visible() []chezmoi.Entry {
	var entries []chezmoi.Entry
	query := strings.ToLower(s.query)
	for _, e := range s.entries {
		if s.changed && !entryChanged(e) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(e.Relative+" "+e.Target+" "+e.Kind), query) {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

func entryChanged(e chezmoi.Entry) bool {
	for _, status := range []string{e.Drift, e.Pending} {
		status = strings.TrimSpace(status)
		if status != "" && status != "?" {
			return true
		}
	}
	return false
}

func entryUnknown(e chezmoi.Entry) bool { return e.Drift == "?" || e.Pending == "?" }

func (m *model) selectedEntry() (chezmoi.Entry, bool) {
	if m.tab > 1 {
		return chezmoi.Entry{}, false
	}
	s := &m.lists[m.tab]
	entries := s.visible()
	if s.selected < 0 || s.selected >= len(entries) {
		return chezmoi.Entry{}, false
	}
	return entries[s.selected], true
}

func (m *model) targets() []string {
	s := &m.lists[0]
	var targets []string
	for _, e := range s.entries {
		if s.checked[e.ID] {
			targets = append(targets, e.Target)
		}
	}
	if len(targets) == 0 {
		if e, ok := m.selectedEntry(); ok {
			targets = append(targets, e.Target)
		}
	}
	return targets
}

func (m *model) report(message string, failed bool) {
	m.status = message
	m.statusErr = failed
	m.results = append(m.results, message)
	if len(m.results) > 30 {
		m.results = m.results[len(m.results)-30:]
	}
}

func (m *model) invalidate() {
	m.refreshGen++
	m.invalidations.latest.Store(m.refreshGen)
	m.pressed = nil
	m.cancelSearch()
	m.search.stale = len(m.search.result.Matches) > 0
	m.scopeGen++
	m.gitGen++
	m.gitStale = m.gitLoaded
	for i := range m.lists {
		s := &m.lists[i]
		s.gen++
		s.statusGen++
		s.statusLoading = false
		s.statusStale = s.statusKnown
		if s.statusCancel != nil {
			s.statusCancel()
		}
		s.sortPending = false
		s.previewGen++
		s.renderGen++
		if s.renderCancel != nil {
			s.renderCancel()
		}
		s.snapshot = nil
		s.stale = s.loaded
		s.loading = false
		s.previewLoading = false
		if s.cancel != nil {
			s.cancel()
		}
		if s.previewCancel != nil {
			s.previewCancel()
		}
	}
}

func (m *model) refresh() tea.Cmd {
	if m.pending != nil {
		return nil
	}
	m.invalidate()
	gen, service, gate := m.refreshGen, m.service, &m.invalidations
	return func() tea.Msg {
		if !gate.invalidate(gen, service) {
			return nil
		}
		return refreshReadyMsg{gen}
	}
}

func (m *model) reloadViews() tea.Cmd {
	cmds := []tea.Cmd{m.loadScope(), m.loadGit()}
	for i := range m.lists {
		if i == m.tab || m.lists[i].loaded {
			cmds = append(cmds, m.loadEntries(i))
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	result, cmd := m.update(msg)
	return result, tea.Batch(cmd, m.flushPendingSorts())
}

func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, handled := m.readinessMessage(msg); handled {
		return m, cmd
	}
	if cmd, handled := m.featureMessage(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.pressed = nil
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		if m.dialog != nil {
			m.dialog.input.SetWidth(max(1, m.width-8))
		}
		return m, m.rerenderDiffs()
	case scopeMsg:
		if msg.gen != m.scopeGen {
			return m, nil
		}
		if msg.err != nil {
			m.scopeErr = msg.err.Error()
		} else {
			m.scope = msg.scope
			m.scopeErr = ""
		}
	case entriesMsg:
		return m, m.acceptInventory(msg)
	case previewMsg:
		s := &m.lists[msg.tab]
		if msg.gen != s.previewGen || msg.id != s.previewID || msg.view != previewViews[s.view] {
			return m, nil
		}
		s.previewLoading = false
		if msg.err != nil {
			s.previewErr = msg.err.Error()
		} else {
			if msg.view == "diff" {
				s.rawDiff = msg.content
				s.snapshot = msg.snapshot
				s.hunkReason = msg.hunkReason
				s.hunkIndex = min(s.hunkIndex, max(0, hunkCount(s)-1))
				return m, m.renderCachedDiff(msg.tab)
			}
			s.preview = msg.content
			s.previewErr = ""
		}
	case gitMsg:
		if msg.gen != m.gitGen {
			return m, nil
		}
		if msg.err != nil {
			m.gitErr = msg.err.Error()
			m.gitStale = m.gitLoaded
			m.report("Git status: "+msg.err.Error(), true)
		} else {
			m.git = msg.git
			m.gitLoaded = true
			m.gitStale = false
			m.gitErr = ""
		}
	case fetchedMsg:
		if msg.gen != m.fetchGen {
			return m, nil
		}
		m.fetching = false
		m.fetchCancel = nil
		if msg.err != nil {
			m.report("Fetch failed: "+msg.err.Error()+"; use : Fetch interactively to retry", true)
		} else {
			m.report("Fetch complete", false)
		}
		if m.pending != nil {
			m.status = m.pending.label + "…"
			m.statusErr = false
			return m, m.prepareOperation()
		}
		return m, m.loadGit()
	case preparedMsg:
		if m.pending == nil || msg.id != m.pending.id {
			return m, nil
		}
		if msg.err != nil {
			return m, m.finishOperation(msg.id, msg.err)
		}
		m.pending.started = time.Now()
		id := msg.id
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg { return operationMsg{id, err} })
	case operationMsg:
		return m, m.finishOperation(msg.id, msg.err)
	case operationCompleteMsg:
		return m, m.completeOperation(msg.id, msg.err)
	case resetMsg:
		if m.pending == nil || msg.id != m.pending.id {
			return m, nil
		}
		if msg.err != nil {
			return m, m.finishOperation(msg.id, msg.err)
		}
		m.pending.resetDone = true
		if m.pending.applyAfterReset {
			m.pending.reset = false
			return m, m.prepareOperation()
		}
		return m, m.finishOperation(msg.id, nil)
	case recordsMsg:
		if m.dialog == nil || m.dialog.kind != "records" || m.dialog.gen != msg.gen {
			return m, nil
		}
		m.dialog.loading = false
		if msg.err != nil {
			m.dialog.err = msg.err.Error()
		} else {
			m.dialog.records = msg.records
			if len(msg.records) == 1 {
				m.dialog.checked[0] = true
			}
		}
	case scriptResultMsg:
		if msg.id != m.operationGen {
			return m, nil
		}
		text := msg.label + ": " + msg.result
		if msg.err != nil {
			text = msg.label + ": outcome unknown (" + msg.err.Error() + ")"
		}
		if msg.operationErr != nil {
			text += "; command failed: " + msg.operationErr.Error()
		}
		m.report(text, msg.err != nil || msg.operationErr != nil)
	case reloadCheckedMsg:
		if msg.gen != m.operationGen || m.pending != nil {
			return m, nil
		}
		if msg.err != nil {
			m.report(msg.err.Error(), true)
			return m, nil
		}
		if msg.operation == nil {
			m.reload = true
			return m, tea.Quit
		}
		return m, m.beginOperation(msg.operation)
	case prefixExpiredMsg:
		if msg.gen == m.prefixGen {
			m.prefix = false
		}
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case tea.PasteMsg:
		if m.dialog != nil && (m.dialog.kind == "filter" || m.dialog.kind == "palette" || m.dialog.kind == "search") {
			msg.Content = strings.NewReplacer("\n", " ", "\r", " ").Replace(sanitize(msg.Content))
			return m, m.updateInput(msg)
		}
	}
	if m.dialog != nil && (m.dialog.kind == "filter" || m.dialog.kind == "palette" || m.dialog.kind == "search") {
		return m, m.updateInput(msg)
	}
	return m, nil
}

func (m *model) move(delta int) tea.Cmd {
	m.pressed = nil
	if m.tab == 3 {
		return m.moveSearch(delta)
	}
	if m.detail && m.tab < 2 {
		s := &m.lists[m.tab]
		s.scroll = max(0, min(s.scroll+delta, max(0, strings.Count(s.preview, "\n"))))
		return nil
	}
	if m.tab == 2 {
		m.maintenanceSelected = max(0, min(m.maintenanceSelected+delta, len(maintenanceActions)-1))
		return nil
	}
	s := &m.lists[m.tab]
	next := max(0, min(s.selected+delta, len(s.visible())-1))
	if next != s.selected {
		s.selected = next
		s.scroll = 0
		return m.loadPreview()
	}
	return nil
}

func (m *model) handleKey(key tea.KeyPressMsg) tea.Cmd {
	m.pressed = nil
	k := key.String()
	if m.dialog != nil {
		return m.dialogKey(key)
	}
	if k == "ctrl+c" {
		if m.fetchCancel != nil {
			m.fetchCancel()
		}
		return tea.Quit
	}
	if k == "q" {
		return tea.Quit
	}
	if k != "g" {
		m.prefix = false
	}
	switch k {
	case "1", "2", "3":
		return m.switchTab(int(k[0] - '1'))
	case "4":
		return m.switchTab(3)
	case "tab", "shift+tab":
		m.detail = !m.detail
		return nil
	case "h", "left":
		m.detail = false
		return nil
	case "l", "right":
		m.detail = true
		return nil
	case "j", "down":
		return m.move(1)
	case "k", "up":
		return m.move(-1)
	case "pgdown", "ctrl+d":
		return m.move(max(1, m.height-9))
	case "pgup", "ctrl+u":
		return m.move(-max(1, m.height-9))
	case "home":
		return m.move(-1 << 30)
	case "end", "G":
		return m.move(1 << 30)
	case "g":
		if m.prefix {
			m.prefix = false
			return m.move(-1 << 30)
		}
		m.prefix = true
		m.prefixGen++
		gen := m.prefixGen
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return prefixExpiredMsg{gen} })
	case "esc":
		if m.maximized {
			m.maximized = false
			return m.rerenderDiffs()
		}
		m.detail = false
		return nil
	case "enter":
		if m.tab == 2 {
			return m.dispatch(maintenanceActions[m.maintenanceSelected])
		}
		m.detail = true
		return nil
	}
	for _, a := range m.actions() {
		if a.key == k && a.enabled {
			if key.IsRepeat && a.write {
				return nil
			}
			return m.dispatch(a.id)
		}
	}
	return nil
}

func (m *model) openInput(kind string) tea.Cmd {
	m.pressed = nil
	in := textinput.New()
	in.Prompt = "/ "
	in.Placeholder = "Filter targets"
	in.SetWidth(max(1, m.width-8))
	in.SetVirtualCursor(true)
	if !m.opts.Color {
		in.SetStyles(plainInputStyles())
	}
	if kind == "palette" {
		in.Prompt = ": "
		in.Placeholder = "Find an action"
	} else if kind == "search" {
		in.Prompt = "Search: "
		in.Placeholder = "Keywords · Enter accepts"
		in.SetValue(m.search.text)
	} else {
		m.detail = false
		in.SetValue(m.lists[m.tab].query)
	}
	m.dialog = &dialog{kind: kind, input: in}
	m.prefix = false
	return m.dialog.input.Focus()
}

func (m *model) updateInput(msg tea.Msg) tea.Cmd {
	d := m.dialog
	old := d.input.Value()
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	if d.input.Value() != old {
		if d.kind == "search" {
			m.search.text = d.input.Value()
			return tea.Batch(cmd, m.scheduleSearch())
		}
		if d.kind == "filter" {
			s := &m.lists[m.tab]
			s.query = d.input.Value()
			s.selected = 0
			s.top = 0
			s.scroll = 0
			return tea.Batch(cmd, m.loadPreview())
		}
		d.selected = 0
	}
	return cmd
}

func (m *model) closeDialog() {
	m.pressed = nil
	if m.dialog != nil && m.dialog.cancel != nil {
		m.dialog.cancel()
	}
	m.dialog = nil
	m.dialogGen++
	m.prefix = false
}

func (m *model) dialogKey(key tea.KeyPressMsg) tea.Cmd {
	d := m.dialog
	k := key.String()
	if k == "esc" || k == "ctrl+c" {
		filter := d.kind == "filter"
		m.closeDialog()
		if filter {
			s := &m.lists[m.tab]
			s.query = ""
			s.selected = 0
			s.scroll = 0
			return m.loadPreview()
		}
		return nil
	}
	switch d.kind {
	case "search":
		if k == "enter" {
			m.closeDialog()
			m.detail = false
			return m.startSearch()
		}
		if k == "up" {
			return m.moveSearch(-1)
		}
		if k == "down" {
			return m.moveSearch(1)
		}
		return m.updateInput(key)
	case "filter":
		if k == "enter" {
			m.closeDialog()
			return nil
		}
		if k == "up" {
			return m.move(-1)
		}
		if k == "down" {
			return m.move(1)
		}
		return m.updateInput(key)
	case "palette":
		items := m.paletteActions()
		if k == "up" {
			d.selected = max(0, d.selected-1)
			return nil
		}
		if k == "down" {
			d.selected = min(max(0, len(items)-1), d.selected+1)
			return nil
		}
		if k == "enter" {
			if len(items) > 0 && d.selected < len(items) {
				id := items[d.selected].id
				m.closeDialog()
				return m.dispatch(id)
			}
			return nil
		}
		return m.updateInput(key)
	case "help", "results", "context":
		if k == "q" || k == "?" {
			m.closeDialog()
			return nil
		}
		if k == "j" || k == "down" {
			d.selected++
			return nil
		}
		if k == "k" || k == "up" {
			d.selected = max(0, d.selected-1)
		}
	case "confirm":
		if k == "down" || k == "j" {
			d.selected++
			return nil
		}
		if k == "up" || k == "k" {
			d.selected = max(0, d.selected-1)
			return nil
		}
		if k == "enter" && !key.IsRepeat {
			op := d.confirm
			m.closeDialog()
			return m.beginOperation(op)
		}
	case "records":
		if d.loading {
			return nil
		}
		if k == "j" || k == "down" {
			d.selected = min(max(0, len(d.records)-1), d.selected+1)
		}
		if k == "k" || k == "up" {
			d.selected = max(0, d.selected-1)
		}
		if k == "space" && len(d.records) > 0 {
			d.checked[d.selected] = !d.checked[d.selected]
		}
		if k == "enter" && !key.IsRepeat {
			var records []chezmoi.ScriptRecord
			for i, r := range d.records {
				if d.checked[i] {
					records = append(records, r)
				}
			}
			if len(records) == 0 {
				d.err = "Select an exact record with Space before continuing"
				return nil
			}
			op := &pendingOperation{label: "Reset script state", entry: d.entry, records: records, reset: true, applyAfterReset: d.applyAfterReset}
			if d.applyAfterReset {
				op.label = "Reset & apply script"
				op.op = chezmoi.Operation{Kind: "script-apply", Targets: []string{d.entry.Source}}
			}
			m.closeDialog()
			m.dialog = &dialog{kind: "confirm", confirm: op}
		}
	}
	return nil
}

func (m *model) beginOperation(op *pendingOperation) tea.Cmd {
	if m.pending != nil {
		return nil
	}
	m.operationGen++
	op.id = m.operationGen
	m.pending = op
	m.invalidate()
	m.report(op.label+"…", false)
	if m.fetching {
		m.status = op.label + " queued; waiting for background fetch"
		if m.fetchCancel != nil {
			m.fetchCancel()
		}
		return nil
	}
	return m.prepareOperation()
}

func (m *model) prepareOperation() tea.Cmd {
	op := *m.pending
	service, ctx := m.service, m.ctx
	effect, feature := m.prepareFeatureOperation(op)
	if !feature {
		if op.reset {
			effect = func() tea.Msg { return resetMsg{op.id, service.ResetScript(ctx, op.entry, op.records)} }
		} else {
			effect = func() tea.Msg { cmd, err := service.Command(ctx, op.op); return preparedMsg{op.id, cmd, err} }
		}
	}
	gate, epoch := &m.invalidations, m.refreshGen
	return func() tea.Msg {
		// Wait for an older, already-entered invalidation to finish before a
		// fresh action starts. Queued obsolete workers fail the epoch check.
		if !gate.fence(epoch) {
			return operationMsg{op.id, context.Canceled}
		}
		return effect()
	}
}

func (m *model) finishOperation(id uint64, err error) tea.Cmd {
	if m.pending == nil || m.pending.id != id {
		return nil
	}
	service, gate, epoch := m.service, &m.invalidations, m.refreshGen
	return func() tea.Msg { gate.invalidate(epoch, service); return operationCompleteMsg{id, err} }
}

func (m *model) completeOperation(id uint64, err error) tea.Cmd {
	if m.pending == nil || m.pending.id != id {
		return nil
	}
	op := *m.pending
	m.pending = nil
	message := op.label + " complete"
	if err != nil {
		message = op.label + " failed: " + err.Error()
		if op.resetDone {
			message = "State reset succeeded; script command failed: " + err.Error()
		}
	}
	m.report(message, err != nil)
	if op.reload && err == nil {
		m.reload = true
		return tea.Quit
	}
	if op.op.Kind == "edit" && m.tab < 2 {
		m.lists[m.tab].view = 3
	}
	cmds := []tea.Cmd{m.reloadViews()}
	if m.search.text != "" {
		cmds = append(cmds, m.scheduleSearch())
	}
	if op.op.Kind == "script-apply" && !op.started.IsZero() {
		service, ctx := m.service, m.ctx
		cmds = append(cmds, func() tea.Msg {
			result, e := service.ScriptResult(ctx, op.entry, op.started)
			return scriptResultMsg{id, op.label, result, e, err}
		})
	}
	return tea.Batch(cmds...)
}

func (m *model) paletteActions() []action {
	query := ""
	if m.dialog != nil {
		query = strings.ToLower(m.dialog.input.Value())
	}
	var result []action
	for _, a := range m.actions() {
		if a.enabled && (query == "" || strings.Contains(strings.ToLower(a.label+" "+a.id), query)) {
			result = append(result, a)
		}
	}
	return result
}

func (m *model) loadRecords(apply bool) tea.Cmd {
	e, ok := m.selectedEntry()
	if !ok {
		return nil
	}
	m.dialogGen++
	ctx, cancel := context.WithCancel(m.ctx)
	m.dialog = &dialog{kind: "records", entry: e, gen: m.dialogGen, cancel: cancel, checked: map[int]bool{}, applyAfterReset: apply, loading: true}
	gen, service := m.dialogGen, m.service
	return func() tea.Msg {
		defer cancel()
		records, err := service.ScriptRecords(ctx, e)
		return recordsMsg{gen, records, err}
	}
}

func (m *model) reloadCommand(op *pendingOperation) tea.Cmd {
	gen := m.operationGen
	return func() tea.Msg { return reloadCheckedMsg{gen, op, shell.Validate(true)} }
}

func errorText(err string) string {
	if err == "" {
		return ""
	}
	return "Error: " + singleLine(err)
}

func entryLabel(e chezmoi.Entry) string {
	if e.Relative != "" {
		return e.Relative
	}
	return e.Target
}

func targetsDescription(targets []string) string {
	if len(targets) == 0 {
		return "all managed targets"
	}
	labels := make([]string, len(targets))
	for i, target := range targets {
		labels[i] = singleLine(target)
	}
	return strings.Join(labels, "\n")
}

func (m *model) String() string { return fmt.Sprintf("tab=%d detail=%t", m.tab, m.detail) }
