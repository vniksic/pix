package ui

import (
	"slices"
	"testing"
	"time"
)

func TestGateStep(t *testing.T) {
	for _, c := range []struct {
		name    string
		loading bool
		since   time.Duration
		pending bool
		want    stepAction
	}{
		{"displayed long ago", false, 300 * time.Millisecond, false, stepNow},
		{"exactly the dwell", false, minDwell, false, stepNow},
		{"just rendered", false, 10 * time.Millisecond, false, stepDefer},
		{"just under the dwell", false, minDwell - time.Millisecond, false, stepDefer},
		{"loading", true, 0, false, stepDefer},
		{"loading, old render time ignored", true, time.Hour, false, stepDefer},
		{"pending while loading", true, 0, true, stepReplace},
		{"pending within the dwell", false, 50 * time.Millisecond, true, stepReplace},
		{"pending, dwell over but timer not run yet", false, time.Second, true, stepReplace},
	} {
		if got := gateStep(c.loading, c.since, c.pending); got != c.want {
			t.Errorf("%s: gateStep(%v, %v, %v) = %v, want %v", c.name, c.loading, c.since, c.pending, got, c.want)
		}
	}
}

func TestDwellLeft(t *testing.T) {
	for _, c := range []struct{ since, want time.Duration }{
		{0, minDwell},
		{30 * time.Millisecond, minDwell - 30*time.Millisecond},
		{minDwell, 0},
		{time.Second, 0},
	} {
		if got := dwellLeft(c.since); got != c.want {
			t.Errorf("dwellLeft(%v) = %v, want %v", c.since, got, c.want)
		}
	}
}

func TestPrefetchOrder(t *testing.T) {
	for _, c := range []struct {
		i, n, dir int
		want      []int
	}{
		{5, 20, 1, []int{6, 7, 8, 9, 4}},
		{5, 20, -1, []int{4, 3, 2, 1, 6}},
		{5, 20, 0, []int{6, 7, 8, 9, 4}},  // no direction yet: forwards
		{5, 20, -7, []int{4, 3, 2, 1, 6}}, // only the sign counts
		{0, 20, 1, []int{1, 2, 3, 4}},     // nothing behind the first
		{0, 20, -1, []int{1}},             // backwards from the first
		{19, 20, 1, []int{18}},            // forwards from the last
		{19, 20, -1, []int{18, 17, 16, 15}},
		{2, 20, -1, []int{1, 0, 3}},
		{17, 20, 1, []int{18, 19, 16}},
		{1, 3, 1, []int{2, 0}},
		{0, 1, 1, nil},
		{0, 0, 1, nil},
	} {
		if got := prefetchOrder(c.i, c.n, c.dir); !slices.Equal(got, c.want) {
			t.Errorf("prefetchOrder(%d, %d, %d) = %v, want %v", c.i, c.n, c.dir, got, c.want)
		}
	}
}
