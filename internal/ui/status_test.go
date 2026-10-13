package ui

import (
	"errors"
	"testing"
	"time"

	"pix/internal/fileops"
)

func TestStatusText(t *testing.T) {
	taken := time.Date(2024, 5, 1, 14, 3, 59, 0, time.UTC)
	img := statusInfo{Label: "sub/a.jpg", Index: 11, Count: 87, HasImg: true, W: 4000, H: 3000, Percent: 33}
	with := func(f func(*statusInfo)) statusInfo { s := img; f(&s); return s }
	for _, c := range []struct {
		name string
		s    statusInfo
		want string
	}{
		{"plain", img, "sub/a.jpg   12/87   4000×3000   33%"},
		{"marked", with(func(s *statusInfo) { s.Marked = true }), "sub/a.jpg ★   12/87   4000×3000   33%"},
		{"date", with(func(s *statusInfo) { s.Taken = taken }), "sub/a.jpg   12/87   4000×3000   2024-05-01 14:03   33%"},
		{"rotated", with(func(s *statusInfo) { s.Rot = 3 }), "sub/a.jpg   12/87   4000×3000   33%   ↻270°"},
		{"lock", with(func(s *statusInfo) { s.Locked = true }), "sub/a.jpg   12/87   4000×3000   33%   lock"},
		{"n marked", with(func(s *statusInfo) { s.NMarked = 3 }), "sub/a.jpg   12/87   4000×3000   33%   3 marked"},
		{"everything", with(func(s *statusInfo) {
			s.Marked, s.Taken, s.Rot, s.Locked, s.NMarked, s.Flash = true, taken, 1, true, 2, "copied"
		}), "sub/a.jpg ★   12/87   4000×3000   2024-05-01 14:03   33%   ↻90°   lock   2 marked   copied"},
		{"loading quietly", with(func(s *statusInfo) { s.Loading, s.Taken, s.NMarked = true, taken, 1 }), "sub/a.jpg   12/87   1 marked"},
		{"loading slowly", with(func(s *statusInfo) { s.Loading, s.Slow = true, true }), "sub/a.jpg   12/87   loading…"},
		{"error", with(func(s *statusInfo) { s.HasImg, s.Err, s.Marked = false, errors.New("bad"), true }), "sub/a.jpg ★   12/87   error: bad"},
		{"empty", statusInfo{Empty: true, Locked: true, Flash: "trashed a.jpg"}, "no images   lock   trashed a.jpg"},
		{"empty plain", statusInfo{Empty: true}, "no images"},
		{"scanning", with(func(s *statusInfo) { s.Scanning, s.NMarked = true, 1 }), "sub/a.jpg   12/87   scanning…   4000×3000   33%   1 marked"},
		{"scanning loading", with(func(s *statusInfo) { s.Scanning, s.Loading, s.Slow = true, true, true }), "sub/a.jpg   12/87   scanning…   loading…"},
		{"scanning empty", statusInfo{Empty: true, Scanning: true, Flash: "rescan: x"}, "scanning…   rescan: x"},
	} {
		if got := statusText(c.s); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestCopySummary(t *testing.T) {
	ok := fileops.CopyResult{Src: "/a", Dst: "/out/a"}
	skip := fileops.CopyResult{Src: "/b", Dst: "/out/b", Skipped: true}
	bad := fileops.CopyResult{Src: "/c", Err: errors.New("disk full")}
	for _, c := range []struct {
		res    []fileops.CopyResult
		want   string
		failed bool
	}{
		{[]fileops.CopyResult{ok, ok, ok}, "copied 3 to ../keep", false},
		{[]fileops.CopyResult{ok, skip, ok}, "copied 2, skipped 1 (already there)", false},
		{[]fileops.CopyResult{skip}, "copied 0, skipped 1 (already there)", false},
		{[]fileops.CopyResult{bad}, "copy failed: disk full", true},
		{[]fileops.CopyResult{ok, bad, skip, bad}, "copied 1, copy failed: disk full (and 1 more)", true},
	} {
		got, failed := copySummary(c.res, "../keep")
		if got != c.want || failed != c.failed {
			t.Errorf("copySummary: got %q %v, want %q %v", got, failed, c.want, c.failed)
		}
	}
}
