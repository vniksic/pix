package ui

import "time"

// minDwell is the least time an image stays on screen while a paging key
// is held: a step arriving sooner after the image was rendered waits.
const minDwell = 100 * time.Millisecond

// stepAction is what a next/previous key press does (see gateStep).
type stepAction int

const (
	stepNow     stepAction = iota // jump right away
	stepDefer                     // remember it as the pending step
	stepReplace                   // replace the pending step (newest direction wins)
)

// gateStep decides what a step does. Paging is gated on display: while the
// current image is still loading, or was rendered less than minDwell ago,
// the step is not taken but remembered as the single pending step, which
// runs once the image has been on screen for minDwell. A step arriving
// while one is already pending replaces it, so a held key advances exactly
// one image per displayed image. sinceRender is ignored while loading.
func gateStep(loading bool, sinceRender time.Duration, hasPending bool) stepAction {
	switch {
	case hasPending:
		return stepReplace
	case loading || sinceRender < minDwell:
		return stepDefer
	default:
		return stepNow
	}
}

// dwellLeft is how long an image rendered sinceRender ago must still stay
// on screen before a pending step may run (0 if none).
func dwellLeft(sinceRender time.Duration) time.Duration {
	return max(0, minDwell-sinceRender)
}

// prefetchOrder is the indices to prefetch after showing i in a list of n,
// most wanted first: four ahead in the paging direction dir (negative means
// backwards, anything else forwards), then one behind. Indices outside the
// list are left out.
func prefetchOrder(i, n, dir int) []int {
	d := 1
	if dir < 0 {
		d = -1
	}
	var out []int
	for _, j := range []int{i + d, i + 2*d, i + 3*d, i + 4*d, i - d} {
		if j >= 0 && j < n {
			out = append(out, j)
		}
	}
	return out
}
