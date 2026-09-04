package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/joshgummersall/summond/internal/job"
	"github.com/spf13/cobra"
)

type tuiTab int

const (
	tabState tuiTab = iota
	tabPlist
	tabLogs
)

type jobsLoadedMsg struct {
	jobs []managedSpec
	err  error
}

type logContentMsg struct {
	content string
	err     error
}

type tickMsg time.Time

type tuiModel struct {
	app        *App
	jobs       []managedSpec
	cursor     int
	listOffset int
	activeTab  tuiTab
	viewport   viewport.Model
	width      int
	height     int
	content    string
	statusMsg  string
}

var (
	activeBorderStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62"))
	inactiveBorderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	selectedItemStyle   = lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230")).Bold(true)
	normalItemStyle     = lipgloss.NewStyle()
	tabActiveStyle      = lipgloss.NewStyle().Bold(true).Underline(true)
	tabInactiveStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	helpStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

const (
	borderOverhead = 4
	tabBarHeight   = 2
	helpBarHeight  = 2
)

func newTUIModel(app *App) tuiModel {
	vp := viewport.New(0, 0)
	vp.SetContent("loading...")
	return tuiModel{
		app:       app,
		activeTab: tabState,
		viewport:  vp,
	}
}

func (a *App) newTUICommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive TUI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := tea.NewProgram(newTUIModel(a), tea.WithAltScreen(), tea.WithMouseCellMotion())
			_, err := p.Run()
			return err
		},
	}
}

func loadJobsCmd(app *App) tea.Cmd {
	return func() tea.Msg {
		jobs, err := app.listManagedJobs()
		return jobsLoadedMsg{jobs: jobs, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(loadJobsCmd(m.app), tickCmd())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewport()
		return m, nil

	case tickMsg:
		return m, tea.Batch(loadJobsCmd(m.app), tickCmd())

	case jobsLoadedMsg:
		if msg.err != nil {
			m.statusMsg = "error: " + msg.err.Error()
			return m, nil
		}
		m.jobs = msg.jobs
		if m.cursor >= len(m.jobs) {
			m.cursor = max(0, len(m.jobs)-1)
		}
		return m, m.loadCurrentContent()

	case logContentMsg:
		if msg.err != nil {
			m.setContent("(no logs: " + msg.err.Error() + ")")
		} else {
			m.setContent(msg.content)
		}
		m.viewport.GotoTop()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "r":
		m.statusMsg = "refreshing..."
		return m, loadJobsCmd(m.app)

	case "[", "shift+tab":
		m.activeTab--
		if m.activeTab < tabState {
			m.activeTab = tabLogs
		}
		return m, m.loadCurrentContent()

	case "]", "tab":
		m.activeTab++
		if m.activeTab > tabLogs {
			m.activeTab = tabState
		}
		return m, m.loadCurrentContent()

	case "X":
		if len(m.jobs) == 0 {
			return m, nil
		}
		managed := m.jobs[m.cursor]
		return m, tea.Exec(execJobProcess(m.app, managed), func(_ error) tea.Msg {
			return loadJobsCmd(m.app)()
		})

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.listOffset = adjustListScroll(m.cursor, m.listOffset, m.listHeight())
			return m, m.loadCurrentContent()
		}

	case "down", "j":
		if m.cursor < len(m.jobs)-1 {
			m.cursor++
			m.listOffset = adjustListScroll(m.cursor, m.listOffset, m.listHeight())
			return m, m.loadCurrentContent()
		}

	}

	return m, nil
}

func (m *tuiModel) loadCurrentContent() tea.Cmd {
	if len(m.jobs) == 0 {
		m.setContent("(no jobs)")
		return nil
	}
	spec := m.jobs[m.cursor].spec

	switch m.activeTab {
	case tabState:
		m.setContent(renderStateContent(spec))
		m.viewport.GotoTop()
		return nil

	case tabPlist:
		if spec.PlistPath == "" {
			m.setContent("(no plist path configured)")
			m.viewport.GotoTop()
			return nil
		}
		data, err := os.ReadFile(spec.PlistPath)
		if err != nil {
			m.setContent(fmt.Sprintf("(plist not available: %s)", err.Error()))
		} else {
			m.setContent(batHighlightPlain(string(data), "xml"))
		}
		m.viewport.GotoTop()
		return nil

	case tabLogs:
		return readLogsCmd(spec)
	}
	return nil
}

func readLogsCmd(spec job.Spec) tea.Cmd {
	return func() tea.Msg {
		var parts []string
		for _, path := range []string{spec.StdoutPath, spec.StderrPath} {
			if path == "" {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					parts = append(parts, fmt.Sprintf("--- %s (not yet created) ---", filepath.Base(path)))
					continue
				}
				return logContentMsg{err: err}
			}
			if len(data) == 0 {
				parts = append(parts, fmt.Sprintf("--- %s (empty) ---", filepath.Base(path)))
				continue
			}
			lines := strings.Split(string(data), "\n")
			const maxLines = 500
			if len(lines) > maxLines {
				lines = lines[len(lines)-maxLines:]
			}
			parts = append(parts, fmt.Sprintf("--- %s ---", filepath.Base(path)))
			parts = append(parts, strings.Join(lines, "\n"))
		}
		if len(parts) == 0 {
			return logContentMsg{content: "(no log paths configured)"}
		}
		return logContentMsg{content: strings.Join(parts, "\n")}
	}
}

func renderStateContent(spec job.Spec) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Name:     %s\n", spec.Name))
	if spec.Group != "" {
		b.WriteString(fmt.Sprintf("Group:    %s\n", spec.Group))
	}
	b.WriteString(fmt.Sprintf("Label:    %s\n", spec.Label))
	b.WriteString(fmt.Sprintf("Target:   %s\n", spec.Target))
	b.WriteString(fmt.Sprintf("Schedule: %s\n", describeTriggerOrSchedule(spec)))
	if spec.Command != "" {
		b.WriteString(fmt.Sprintf("Command:  %s\n", spec.Command))
		if len(spec.Args) > 0 {
			b.WriteString(fmt.Sprintf("Args:     %s\n", strings.Join(spec.Args, " ")))
		}
	}
	if spec.WorkingDir != "" {
		b.WriteString(fmt.Sprintf("Dir:      %s\n", spec.WorkingDir))
	}
	if spec.ShellCommand != "" {
		b.WriteString("Shell:    ")
		b.WriteString(strings.Trim(batHighlight(spec.ShellCommand, "bash"), "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("--- Last Run ---\n")
	b.WriteString(fmt.Sprintf("Status:   %s\n", describeLastRunStatus(spec)))
	if spec.LastStartedAt != nil {
		b.WriteString(fmt.Sprintf("Started:  %s\n", formatTimestamp(*spec.LastStartedAt)))
	}
	if spec.LastFinishedAt != nil {
		b.WriteString(fmt.Sprintf("Finished: %s\n", formatTimestamp(*spec.LastFinishedAt)))
	}
	if spec.LastError != "" {
		b.WriteString(fmt.Sprintf("Error:    %s\n", spec.LastError))
	}
	b.WriteString("\n")

	b.WriteString("--- Counts ---\n")
	b.WriteString(fmt.Sprintf("Total:    %d\n", spec.RunCount))
	b.WriteString(fmt.Sprintf("Success:  %d\n", spec.SuccessCount))
	b.WriteString(fmt.Sprintf("Failure:  %d\n", spec.FailureCount))

	if len(spec.RecentRuns) > 0 {
		b.WriteString("\n--- Recent Runs ---\n")
		for _, run := range spec.RecentRuns {
			line := formatTimestamp(run.StartedAt)
			if run.ExitCode != nil {
				line += fmt.Sprintf(" exit=%d", *run.ExitCode)
			}
			if run.Error != "" {
				line += " err=" + run.Error
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n--- Paths ---\n")
	if spec.PlistPath != "" {
		b.WriteString(fmt.Sprintf("Plist:    %s\n", spec.PlistPath))
	}
	if spec.StdoutPath != "" {
		b.WriteString(fmt.Sprintf("Stdout:   %s\n", spec.StdoutPath))
	}
	if spec.StderrPath != "" {
		b.WriteString(fmt.Sprintf("Stderr:   %s\n", spec.StderrPath))
	}

	return b.String()
}

// batHighlight pipes src through bat and indents output to align with the
// value column. Falls back to indented plain text if bat is not installed.
func batHighlight(src, lang string) string {
	return indent(batRaw(src, lang))
}

// batHighlightPlain pipes src through bat without any extra indentation.
// Falls back to plain text if bat is not installed.
func batHighlightPlain(src, lang string) string {
	return batRaw(src, lang)
}

func batRaw(src, lang string) string {
	bat, err := exec.LookPath("bat")
	if err != nil {
		return src
	}
	var out bytes.Buffer
	cmd := exec.Command(bat, "--language="+lang, "--color=always", "--style=plain", "--paging=never")
	cmd.Stdin = strings.NewReader(src)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return src
	}
	return out.String()
}

func indent(s string) string {
	const prefix = "          " // 10 spaces, aligns with "Schedule: "
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if i == 0 || l == "" {
			continue
		}
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) leftPaneWidth() int {
	return int(float64(m.width) * 0.28)
}

func (m tuiModel) rightPaneWidth() int {
	return m.width - m.leftPaneWidth() - borderOverhead
}

func (m tuiModel) innerHeight() int {
	return m.height - helpBarHeight - borderOverhead
}

func (m tuiModel) viewportHeight() int {
	h := m.innerHeight() - tabBarHeight
	if h < 1 {
		return 1
	}
	return h
}

func (m tuiModel) listHeight() int {
	h := m.innerHeight()
	if h < 1 {
		return 1
	}
	return h
}

func (m *tuiModel) resizeViewport() {
	w := m.rightPaneWidth() - borderOverhead
	if w < 1 {
		w = 1
	}
	m.viewport.Width = w
	m.viewport.Height = m.viewportHeight()
	m.applyContent()
}

// setContent stores sanitized pane content and pushes it to the viewport.
func (m *tuiModel) setContent(s string) {
	m.content = sanitizeContent(s)
	m.applyContent()
}

// applyContent re-truncates the stored content for the current viewport width.
func (m *tuiModel) applyContent() {
	m.viewport.SetContent(fitWidth(m.content, m.viewport.Width))
}

func adjustListScroll(cursor, offset, height int) int {
	if cursor < offset {
		return cursor
	}
	if cursor >= offset+height {
		return cursor - height + 1
	}
	return offset
}

func (m tuiModel) renderJobList(height int) string {
	if len(m.jobs) == 0 {
		return "(no jobs)"
	}
	start := m.listOffset
	end := start + height
	if end > len(m.jobs) {
		end = len(m.jobs)
	}
	var lines []string
	for i := start; i < end; i++ {
		spec := m.jobs[i].spec
		label := spec.ManagedKey()
		if spec.Target == job.TargetDaemon {
			label += " (daemon)"
		}
		label = ansi.Truncate(sanitizeLine(label), m.leftPaneWidth(), "")
		if i == m.cursor {
			lines = append(lines, selectedItemStyle.Width(m.leftPaneWidth()).Render(label))
		} else {
			lines = append(lines, normalItemStyle.Render(label))
		}
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderTabs() string {
	type tabDef struct {
		label string
		tab   tuiTab
	}
	tabs := []tabDef{
		{"State", tabState},
		{"Plist", tabPlist},
		{"Logs", tabLogs},
	}
	var parts []string
	for _, t := range tabs {
		if t.tab == m.activeTab {
			parts = append(parts, tabActiveStyle.Render(t.label))
		} else {
			parts = append(parts, tabInactiveStyle.Render(t.label))
		}
	}
	return strings.Join(parts, "  ")
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "loading..."
	}

	leftWidth := m.leftPaneWidth()
	rightWidth := m.rightPaneWidth()
	innerH := m.innerHeight()

	leftContent := m.renderJobList(m.listHeight())
	leftPane := inactiveBorderStyle.Width(leftWidth).Height(innerH).Render(leftContent)

	tabs := m.renderTabs()
	rightContent := lipgloss.JoinVertical(lipgloss.Left, tabs, m.viewport.View())
	rightPane := inactiveBorderStyle.Width(rightWidth).Height(innerH).Render(rightContent)

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	help := helpStyle.Render("↑↓/jk:navigate  tab/shift+tab:cycle-tab  X:exec  r:refresh  q:quit")

	var lines []string
	lines = append(lines, body)
	if m.statusMsg != "" {
		lines = append(lines, m.statusMsg)
	}
	lines = append(lines, help)
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func execJobProcess(app *App, managed managedSpec) *execProcess {
	return &execProcess{app: app, managed: managed}
}

type execProcess struct {
	app     *App
	managed managedSpec
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

func (e *execProcess) SetStdin(r io.Reader)  { e.stdin = r }
func (e *execProcess) SetStdout(w io.Writer) { e.stdout = w }
func (e *execProcess) SetStderr(w io.Writer) { e.stderr = w }

func (e *execProcess) Run() error {
	spec := e.managed.spec

	saved := struct {
		in       io.Reader
		out, err io.Writer
	}{e.app.stdin, e.app.stdout, e.app.stderr}
	e.app.stdin, e.app.stdout, e.app.stderr = e.stdin, e.stdout, e.stderr
	defer func() { e.app.stdin, e.app.stdout, e.app.stderr = saved.in, saved.out, saved.err }()

	changedPaths, newFingerprints := watchChanges(spec, e.managed.store)
	startedAt := time.Now()
	if err := e.managed.store.RecordExecutionStart(spec.Name, startedAt); err != nil {
		return err
	}
	record := job.ExecutionRecord{StartedAt: startedAt}

	maxAttempts := 1 + spec.RetryAttempts
	delay := time.Duration(spec.RetryDelaySeconds) * time.Second
	exitCode, runErr := 0, error(nil)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
			if spec.RetryMaxDelaySeconds > 0 {
				if cap := time.Duration(spec.RetryMaxDelaySeconds) * time.Second; delay > cap {
					delay = cap
				}
			}
		}
		exitCode, runErr = e.app.executeSpec(spec, changedPaths, attempt)
		if runErr == nil {
			break
		}
	}

	finishedAt := time.Now()
	record.FinishedAt = &finishedAt
	record.ExitCode = &exitCode
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if err := e.managed.store.RecordExecutionFinish(spec.Name, record); err != nil {
		return err
	}
	if runErr == nil && len(newFingerprints) > 0 {
		_ = e.managed.store.UpdateWatchFingerprints(spec.Name, newFingerprints)
	}
	return runErr
}
