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
	rot := fileops.CopyResult{Src: "/d", Dst: "/out/d", Rotated: true}
	png := fileops.CopyResult{Src: "/e", Dst: "/out/e", RotLost: "png"}
	heic := fileops.CopyResult{Src: "/f", Dst: "/out/f", RotLost: "heic"}
	skipRot := fileops.CopyResult{Src: "/g", Dst: "/out/g", Skipped: true, Rotated: true}
	skipPNG := fileops.CopyResult{Src: "/h", Dst: "/out/h", Skipped: true, RotLost: "png"}
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
		{[]fileops.CopyResult{rot, ok, rot}, "copied 3 to ../keep (2 rotated)", false},
		{[]fileops.CopyResult{ok, png, rot}, "copied 3 to ../keep (1 rotated), rotation not stored for 1 (png)", false},
		{[]fileops.CopyResult{heic, png, png}, "copied 3 to ../keep, rotation not stored for 3 (heic, png)", false},
		{[]fileops.CopyResult{rot, skipRot, skipPNG}, "copied 1 (1 rotated), skipped 2 (already there), rotation not stored for 1 (png)", false},
		{[]fileops.CopyResult{rot, bad, png}, "copied 2 (1 rotated), copy failed: disk full", true},
	} {
		got, failed := copySummary(c.res, "../keep")
		if got != c.want || failed != c.failed {
			t.Errorf("copySummary: got %q %v, want %q %v", got, failed, c.want, c.failed)
		}
	}
}
