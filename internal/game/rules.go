package game

// DetectCaptures returns every capture triggered by `by` placing a stone at
// `from`. A capture is the pattern [by][victim][victim][by] on one of the
// four axes, with both victim cells sharing the same non-by color.
//
// The just-placed stone must be one of the two flankers — only patterns that
// `from` directly closes are considered. As a corollary, "suicide" placements
// (filling the middle of an opponent sandwich) do not capture the placer:
// the algorithm scans for `by` flankers, not `by` victims.
func DetectCaptures(b *Board, from Position, by Color) []Capture {
	var out []Capture
	for _, d := range directions {
		// `from` can play either role of the [by][_][_][by] flankers.
		if c, ok := tryCapture(b, from, d, by); ok {
			out = append(out, c)
		}
		leftStart := Position{from.Q - 3*d.Q, from.R - 3*d.R}
		if c, ok := tryCapture(b, leftStart, d, by); ok {
			out = append(out, c)
		}
	}
	return out
}

// tryCapture checks for [by][victim][victim][by] starting at p1, in direction d.
func tryCapture(b *Board, p1, d Position, by Color) (Capture, bool) {
	p2 := Position{p1.Q + d.Q, p1.R + d.R}
	p3 := Position{p1.Q + 2*d.Q, p1.R + 2*d.R}
	p4 := Position{p1.Q + 3*d.Q, p1.R + 3*d.R}
	if !b.In(p1) || !b.In(p4) {
		return Capture{}, false
	}
	if b.At(p1) != by || b.At(p4) != by {
		return Capture{}, false
	}
	v := b.At(p2)
	if v == Empty || v == by || v != b.At(p3) {
		return Capture{}, false
	}
	return Capture{Capturer: by, Victim: v, Pair: [2]Position{p2, p3}}, true
}

// HasRun reports whether any maximal run of `color` is at least `minLen`
// cells long.
func HasRun(b *Board, color Color, minLen int) bool {
	found := false
	forEachMaximalRun(b, color, func(length int) bool {
		if length >= minLen {
			found = true
			return false
		}
		return true
	})
	return found
}

// CountMaximalRuns returns the number of maximal runs of `color` whose length
// is exactly `length`. A maximal run is a straight unbroken segment of
// `color` that cannot be extended on either end (the cell before is off-board
// or holds a different color, same for the cell after).
func CountMaximalRuns(b *Board, color Color, length int) int {
	n := 0
	forEachMaximalRun(b, color, func(l int) bool {
		if l == length {
			n++
		}
		return true
	})
	return n
}

// forEachMaximalRun walks every maximal run of `color` exactly once and
// invokes `fn` with its length. Returning false from `fn` stops iteration.
func forEachMaximalRun(b *Board, color Color, fn func(length int) bool) {
	r := b.Side - 1
	for q := -r; q <= r; q++ {
		for s := -r; s <= r; s++ {
			p := Position{Q: q, R: s}
			if !b.In(p) || b.At(p) != color {
				continue
			}
			for _, d := range directions {
				// Only start walking at the origin of a maximal run.
				prev := Position{p.Q - d.Q, p.R - d.R}
				if b.In(prev) && b.At(prev) == color {
					continue
				}
				length := 0
				for c := p; b.In(c) && b.At(c) == color; c = (Position{c.Q + d.Q, c.R + d.R}) {
					length++
				}
				if !fn(length) {
					return
				}
			}
		}
	}
}
