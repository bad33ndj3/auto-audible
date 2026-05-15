package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#333")).
			Padding(0, 1).
			MarginBottom(1)

	readyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#04B575"))

	busyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3498DB"))

	pendingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7F8C8D"))

	needsConvertStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F1C40F"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E74C3C"))

	downloadedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9B59B6"))

	statsStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#BDC3C7")).
			MarginTop(1)

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7F8C8D")).
			MarginTop(1)

	boxStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#555")).
			Padding(1, 2)
)

func styleForState(s BookState) lipgloss.Style {
	switch s {
	case StateReady:
		return readyStyle
	case StateDownloading, StateConverting:
		return busyStyle
	case StateNeedsConvert:
		return needsConvertStyle
	case StateError:
		return errorStyle
	case StateDownloaded:
		return downloadedStyle
	default:
		return pendingStyle
	}
}

// tuiBook is a book as displayed in the TUI.
type tuiBook struct {
	ASIN    string
	Title   string
	State   BookState
	Message string
}

// tuiStats holds aggregate counts.
type tuiStats struct {
	total       int
	ready       int
	downloading int
	converting  int
	pending     int
	failed      int
}

const maxVisibleBooks = 10

// tuiModel is the Bubble Tea model for the auto-audible TUI.
type tuiModel struct {
	app             *App
	cmd             string
	width           int
	height          int
	books           []tuiBook
	bookIdx         map[string]int
	scrollOffset    int
	stats           tuiStats
	overallPercent  int
	speed           string
	spinner         spinner.Model
	quitting        bool
	done            bool
	err             error
	progress        chan progressEvent
	logs            []string
}

func newTUIModel(app *App, cmd string) tuiModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#3498DB"))
	return tuiModel{
		app:      app,
		cmd:      cmd,
		spinner:  s,
		progress: make(chan progressEvent, 64),
		bookIdx:  make(map[string]int),
	}
}

func (m tuiModel) Init() tea.Cmd {
	// Start the background work.
	go m.runBackground()
	return tea.Batch(
		m.spinner.Tick,
		waitForProgress(m.progress),
	)
}

func (m tuiModel) runBackground() {
	ctx := context.Background()
	m.app.Progress = newChannelProgress(m.progress)

	// Suppress audible-cli and ffmpeg output so it doesn't overwrite the TUI.
	if cli, ok := m.app.Audible.(*audibleCLI); ok {
		cli.SetSilent(true)
	}
	if conv, ok := m.app.Converter.(*ffmpegConverter); ok {
		conv.SetSilent(true)
	}

	switch m.cmd {
	case "status":
		m.runStatus(ctx)
	case "download":
		_ = m.app.Download(ctx)
	case "convert":
		_ = m.app.Convert(ctx)
	case "all":
		if err := m.app.Download(ctx); err != nil {
			m.app.progress().Log(fmt.Sprintf("download failed: %v", err))
		}
		if err := m.app.Convert(ctx); err != nil {
			m.app.progress().Log(fmt.Sprintf("convert failed: %v", err))
		}
	}
}

func (m tuiModel) runStatus(ctx context.Context) {
	items, err := m.app.Audible.ExportLibrary(ctx)
	if err != nil {
		m.app.progress().Log(fmt.Sprintf("library export failed: %v", err))
		m.app.progress().Done()
		return
	}

	downloaded, _ := m.app.Store.Load()
	downloadedSet := make(map[string]struct{}, len(downloaded))
	for _, asin := range downloaded {
		downloadedSet[asin] = struct{}{}
	}

	index, _ := m.app.buildASINMediaIndex()

	for _, item := range items {
		asin := strings.TrimSpace(item.ASIN)
		if asin == "" {
			continue
		}
		_, tracked := downloadedSet[asin]
		state := computeBookState(tracked, index[asin])
		var bs BookState
		switch state {
		case "ready":
			bs = StateReady
		case "needs_convert_aax", "needs_convert_aaxc":
			bs = StateNeedsConvert
		case "tracked_no_media":
			bs = StateDownloaded
		case "not_downloaded":
			bs = StatePending
		default:
			bs = StatePending
		}
		m.app.progress().UpdateBook(BookProgress{ASIN: asin, Title: item.Title, State: bs})
	}

	m.app.progress().Done()
}

func waitForProgress(ch chan progressEvent) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return progressEvent{kind: eventDone}
		}
		return e
	}
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.scrollOffset > 0 {
				m.scrollOffset--
			}
			return m, nil
		case "down", "j":
			if m.scrollOffset < len(m.books)-maxVisibleBooks {
				m.scrollOffset++
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case progressEvent:
		switch msg.kind {
		case eventLibrary:
			m.books = make([]tuiBook, 0, len(msg.items))
			m.bookIdx = make(map[string]int, len(msg.items))
			m.stats.total = len(msg.items)
			for _, item := range msg.items {
				m.addOrUpdateBook(tuiBook{ASIN: item.ASIN, Title: item.Title, State: StatePending})
			}
		case eventBook:
			if msg.book != nil {
				m.addOrUpdateBook(tuiBook{
					ASIN:    msg.book.ASIN,
					Title:   msg.book.Title,
					State:   msg.book.State,
					Message: msg.book.Message,
				})
			}
		case eventStats:
			m.stats = tuiStats{
				total:       msg.total,
				ready:       msg.ready,
				downloading: msg.downloading,
				converting:  msg.converting,
				pending:     msg.pending,
				failed:      msg.failed,
			}
		case eventOverallProgress:
			m.overallPercent = msg.percent
			m.speed = msg.speed
		case eventLog:
			if msg.message != "" {
				m.logs = append(m.logs, msg.message)
				if len(m.logs) > 10 {
					m.logs = m.logs[len(m.logs)-10:]
				}
			}
		case eventDone:
			m.done = true
			return m, nil
		}
		return m, waitForProgress(m.progress)
	}

	return m, nil
}

func (m *tuiModel) addOrUpdateBook(b tuiBook) {
	if idx, ok := m.bookIdx[b.ASIN]; ok {
		m.books[idx] = b
	} else {
		m.bookIdx[b.ASIN] = len(m.books)
		m.books = append(m.books, b)
	}
}

func (m tuiModel) View() string {
	if m.err != nil {
		return boxStyle.Render(errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
	}

	var b strings.Builder

	// Title bar.
	title := "Auto-Audible"
	if m.cmd != "" {
		title = fmt.Sprintf("Auto-Audible -- %s", strings.ToUpper(m.cmd))
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	// Overall progress line.
	if m.stats.total > 0 {
		parts := []string{}
		parts = append(parts, fmt.Sprintf("%d books", m.stats.total))
		if m.overallPercent > 0 {
			parts = append(parts, fmt.Sprintf("%d%%", m.overallPercent))
		}
		if m.speed != "" {
			parts = append(parts, m.speed)
		}
		b.WriteString(statsStyle.Render(strings.Join(parts, " | ")))
		b.WriteString("\n")
	}

	// Book list.
	if len(m.books) == 0 {
		b.WriteString(pendingStyle.Render("Loading..."))
		b.WriteString("\n")
	} else {
		start := m.scrollOffset
		if start < 0 {
			start = 0
		}
		end := start + maxVisibleBooks
		if end > len(m.books) {
			end = len(m.books)
		}
		for i := start; i < end; i++ {
			line := m.renderBookLine(m.books[i])
			b.WriteString(line)
			b.WriteString("\n")
		}
		if len(m.books) > maxVisibleBooks {
			remaining := len(m.books) - maxVisibleBooks
			b.WriteString(pendingStyle.Render(fmt.Sprintf("  ... and %d more (use j/k or up/down to scroll)", remaining)))
			b.WriteString("\n")
		}
	}

	// State summary line.
	if m.stats.total > 0 {
		parts := []string{}
		if m.stats.ready > 0 {
			parts = append(parts, readyStyle.Render(fmt.Sprintf("%d ready", m.stats.ready)))
		}
		busy := m.stats.downloading + m.stats.converting
		if busy > 0 {
			parts = append(parts, busyStyle.Render(fmt.Sprintf("%d busy", busy)))
		}
		if m.stats.pending > 0 {
			parts = append(parts, pendingStyle.Render(fmt.Sprintf("%d pending", m.stats.pending)))
		}
		if m.stats.failed > 0 {
			parts = append(parts, errorStyle.Render(fmt.Sprintf("%d failed", m.stats.failed)))
		}
		if len(parts) > 0 {
			b.WriteString(statsStyle.Render(strings.Join(parts, " | ")))
			b.WriteString("\n")
		}
	}

	// Footer.
	if m.done {
		b.WriteString(footerStyle.Render("Done. Press q to quit.  |  j/k scroll"))
	} else {
		b.WriteString(footerStyle.Render("Press q to quit  |  j/k scroll"))
	}

	return boxStyle.Render(b.String())
}

func (m tuiModel) renderBookLine(book tuiBook) string {
	icon := stateIcon(book.State)
	label := stateLabel(book.State)
	st := styleForState(book.State)

	prefix := fmt.Sprintf("[%s %s]", icon, label)
	prefix = st.Render(prefix)

	title := book.Title
	if len(title) > 50 {
		title = title[:47] + "..."
	}

	line := fmt.Sprintf("  %-22s %s", prefix, title)

	if book.Message != "" && book.State == StateError {
		msg := book.Message
		if len(msg) > 30 {
			msg = msg[:27] + "..."
		}
		line += errorStyle.Render(fmt.Sprintf("  (%s)", msg))
	}

	// Add spinner for busy items.
	if book.State == StateDownloading || book.State == StateConverting {
		line = fmt.Sprintf("  %s %-18s %s", m.spinner.View(), prefix, title)
	}

	return line
}

// runTUI starts the TUI for the given command.
func runTUI(app *App, cmd string) error {
	m := newTUIModel(app, cmd)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
