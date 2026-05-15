package main

import (
	"fmt"
	"sync"
)

// BookState represents the current state of a book in the UI.
type BookState string

const (
	StatePending      BookState = "pending"
	StateDownloading  BookState = "downloading"
	StateDownloaded   BookState = "downloaded"
	StateConverting   BookState = "converting"
	StateReady        BookState = "ready"
	StateNeedsConvert BookState = "needs_convert"
	StateError        BookState = "error"
)

// BookProgress holds the current progress for a single book.
type BookProgress struct {
	ASIN    string
	Title   string
	State   BookState
	Message string
}

// ProgressReporter is called by App to report real-time progress.
type ProgressReporter interface {
	SetLibrary(items []Book)
	UpdateBook(p BookProgress)
	SetStats(total, ready, downloading, converting, pending, failed int)
	SetOverallProgress(percent int, speed string)
	Log(msg string)
	Done()
}

// noopProgress is a no-op reporter used when no TUI is active.
type noopProgress struct{}

func (n *noopProgress) SetLibrary(items []Book)                                             {}
func (n *noopProgress) UpdateBook(p BookProgress)                                           {}
func (n *noopProgress) SetStats(total, ready, downloading, converting, pending, failed int) {}
func (n *noopProgress) SetOverallProgress(percent int, speed string)                        {}
func (n *noopProgress) Log(msg string)                                                      {}
func (n *noopProgress) Done()                                                               {}

// channelProgress sends progress updates over a channel for the TUI.
type channelProgress struct {
	ch     chan progressEvent
	once   sync.Once
}

func newChannelProgress(ch chan progressEvent) *channelProgress {
	return &channelProgress{ch: ch}
}

func (c *channelProgress) SetLibrary(items []Book) {
	c.send(progressEvent{kind: eventLibrary, items: items})
}

func (c *channelProgress) UpdateBook(p BookProgress) {
	c.send(progressEvent{kind: eventBook, book: &p})
}

func (c *channelProgress) SetStats(total, ready, downloading, converting, pending, failed int) {
	c.send(progressEvent{
		kind:        eventStats,
		total:       total,
		ready:       ready,
		downloading: downloading,
		converting:  converting,
		pending:     pending,
		failed:      failed,
	})
}

func (c *channelProgress) SetOverallProgress(percent int, speed string) {
	c.send(progressEvent{
		kind:    eventOverallProgress,
		percent: percent,
		speed:   speed,
	})
}

func (c *channelProgress) Log(msg string) {
	c.send(progressEvent{kind: eventLog, message: msg})
}

func (c *channelProgress) Done() {
	c.once.Do(func() {
		c.send(progressEvent{kind: eventDone})
		close(c.ch)
	})
}

func (c *channelProgress) send(e progressEvent) {
	select {
	case c.ch <- e:
	default:
	}
}

// progressEvent is the internal message sent from App to the TUI.
type progressEvent struct {
	kind        eventKind
	items       []Book
	book        *BookProgress
	total       int
	ready       int
	downloading int
	converting  int
	pending     int
	failed      int
	percent     int
	speed       string
	message     string
}

type eventKind int

const (
	eventLibrary eventKind = iota
	eventBook
	eventStats
	eventLog
	eventDone
	eventOverallProgress
)

// stateColor returns a hex color for a given state.
func stateColor(s BookState) string {
	switch s {
	case StateReady:
		return "#04B575"
	case StateDownloading, StateConverting:
		return "#3498DB"
	case StateNeedsConvert:
		return "#F1C40F"
	case StateError:
		return "#E74C3C"
	case StatePending:
		return "#7F8C8D"
	case StateDownloaded:
		return "#9B59B6"
	default:
		return "#7F8C8D"
	}
}

// stateLabel returns a short label for a state.
func stateLabel(s BookState) string {
	switch s {
	case StateReady:
		return "ready"
	case StateDownloading:
		return "downloading"
	case StateConverting:
		return "converting"
	case StateNeedsConvert:
		return "needs convert"
	case StateError:
		return "error"
	case StateDownloaded:
		return "downloaded"
	case StatePending:
		return "pending"
	default:
		return string(s)
	}
}

// stateIcon returns an icon-like prefix for a state.
func stateIcon(s BookState) string {
	switch s {
	case StateReady:
		return ">"
	case StateDownloading, StateConverting:
		return "~"
	case StateNeedsConvert:
		return "!"
	case StateError:
		return "x"
	case StateDownloaded:
		return "+"
	case StatePending:
		return "-"
	default:
		return "?"
	}
}

// formatProgress returns a percentage string or empty if zero.
func formatProgress(current, total int) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf(" %.0f%%", float64(current)/float64(total)*100)
}
