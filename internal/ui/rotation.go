package ui

// rotationFor decides the rotation (clockwise quarter turns, 0..3) of the
// image at path when it is shown: the rotation remembered for that path in
// this session, if any (set by `[` / `]`; 0 counts as remembered);
// otherwise, when the view is locked, current, the rotation carried over
// from the previous image; otherwise 0.
func rotationFor(remembered map[string]int, path string, locked bool, current int) int {
	if q, ok := remembered[path]; ok {
		return NormQuarter(q)
	}
	if locked {
		return NormQuarter(current)
	}
	return 0
}
