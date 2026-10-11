package ui

import (
	"fmt"
	"strings"
	"time"

	"pix/internal/fileops"
)

// takenLayout formats the date a picture was taken in the status line.
const takenLayout = "2006-01-02 15:04"

// statusInfo is everything the status line shows; statusText composes it.
type statusInfo struct {
	Empty   bool // no images: everything up to the image details is replaced by "no images"
	Label   string
	Marked  bool // the current entry is marked
	Index   int  // 0-based position of the current entry
	Count   int  // entries in the playlist
	Loading bool // a decode is in progress (details hidden)
	Slow    bool // ... and has taken long enough to say so
	Err     error
	HasImg  bool // image details below are valid
	W, H    int
	Taken   time.Time // zero when unknown
	Percent int
	Rot     int // clockwise quarter turns
	Locked  bool
	NMarked int
	Flash   string
}

// statusText composes the status line:
//
//	label ★   12/87   4000×3000   2024-05-01 14:03   33%   ↻90°   lock   3 marked   <flash>
//
// Parts that do not apply are left out.
func statusText(s statusInfo) string {
	var parts []string
	if s.Empty {
		parts = append(parts, "no images")
	} else {
		label := s.Label
		if s.Marked {
			label += " ★"
		}
		parts = append(parts, label, fmt.Sprintf("%d/%d", s.Index+1, s.Count))
		switch {
		case s.Loading:
			if s.Slow {
				parts = append(parts, "loading…")
			}
		case s.Err != nil:
			parts = append(parts, "error: "+s.Err.Error())
		case s.HasImg:
			parts = append(parts, fmt.Sprintf("%d×%d", s.W, s.H))
			if !s.Taken.IsZero() {
				parts = append(parts, s.Taken.Format(takenLayout))
			}
			parts = append(parts, fmt.Sprintf("%d%%", s.Percent))
			if r := NormQuarter(s.Rot); r != 0 {
				parts = append(parts, fmt.Sprintf("↻%d°", r*90))
			}
		}
	}
	if s.Locked {
		parts = append(parts, "lock")
	}
	if s.NMarked > 0 {
		parts = append(parts, fmt.Sprintf("%d marked", s.NMarked))
	}
	if s.Flash != "" {
		parts = append(parts, s.Flash)
	}
	return strings.Join(parts, "   ")
}

// copySummary describes the outcome of copying marked files to dir:
// "copied 3 to DIR", "copied 2, skipped 1 (already there)", or, when a copy
// failed, the first error (failed is then true).
func copySummary(res []fileops.CopyResult, dir string) (msg string, failed bool) {
	copied, skipped, nerr := 0, 0, 0
	var first error
	for _, r := range res {
		switch {
		case r.Err != nil:
			nerr++
			if first == nil {
				first = r.Err
			}
		case r.Skipped:
			skipped++
		default:
			copied++
		}
	}
	switch {
	case first != nil:
		msg = "copy failed: " + first.Error()
		if nerr > 1 {
			msg += fmt.Sprintf(" (and %d more)", nerr-1)
		}
		if copied > 0 {
			msg = fmt.Sprintf("copied %d, ", copied) + msg
		}
		return msg, true
	case skipped > 0:
		return fmt.Sprintf("copied %d, skipped %d (already there)", copied, skipped), false
	}
	return fmt.Sprintf("copied %d to %s", copied, dir), false
}
