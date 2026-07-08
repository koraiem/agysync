package sync

import (
	"fmt"
	"strings"
)

// ProgressTracker implements a dynamic terminal progress bar
type ProgressTracker struct {
	Label   string
	Current int
	Total   int
}

// Print updates the progress bar on the current terminal line
func (p *ProgressTracker) Print() {
	if p.Total <= 0 {
		return
	}
	percent := (p.Current * 100) / p.Total
	if percent > 100 {
		percent = 100
	}
	barWidth := 25
	filled := (percent * barWidth) / 100
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", barWidth-filled)

	// Throttle to print every 10 items or on completion to avoid slowing down operations
	if p.Current%10 == 0 || p.Current == p.Total {
		fmt.Printf("\r%s: [%s] %d%% (%d/%d)", p.Label, bar, percent, p.Current, p.Total)
	}
}

// Finish prints the 100% completed state and adds a new line
func (p *ProgressTracker) Finish() {
	p.Current = p.Total
	p.Print()
	fmt.Println()
}
