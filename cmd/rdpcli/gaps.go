package main

import (
	"fmt"
	"sort"
	"time"
)

// gapStats measures when bitmap updates arrive relative to each other.
//
// It exists to answer one question: in a session that carries only bitmap
// updates, is a visual frame one update, or a burst of them separated by a gap?
// The answer decides whether presenting on "the update is complete" is enough, or
// whether the presenter has to wait for a quiet moment before showing anything.
type gapStats struct {
	times   []time.Time
	rects   []int
	first   time.Time
	inBurst int
	bursts  int
}

func (g *gapStats) update(n int) {
	now := time.Now()
	if g.first.IsZero() {
		g.first = now
	}
	g.times = append(g.times, now)
	g.rects = append(g.rects, n)
}

// report prints the number of updates, the gaps between them, and how the updates
// group into bursts at a few quiet thresholds. A burst is a run of updates whose
// neighbours are closer together than the threshold.
func (g *gapStats) report() {
	if len(g.times) < 2 {
		fmt.Printf("gaps: %d updates, too few to measure\n", len(g.times))
		return
	}
	gaps := make([]float64, 0, len(g.times)-1)
	var totalRects int
	for i, n := range g.rects {
		totalRects += n
		if i == 0 {
			continue
		}
		gaps = append(gaps, float64(g.times[i].Sub(g.times[i-1]).Microseconds())/1000.0)
	}
	sorted := append([]float64(nil), gaps...)
	sort.Float64s(sorted)
	pct := func(p float64) float64 {
		i := int(p * float64(len(sorted)-1))
		return sorted[i]
	}
	span := g.times[len(g.times)-1].Sub(g.times[0])
	fmt.Printf("gaps: %d updates, %d rects, span %v\n", len(g.times), totalRects, span.Round(time.Millisecond))
	fmt.Printf("  gap ms: min %.1f p25 %.1f median %.1f p75 %.1f p90 %.1f max %.1f\n",
		sorted[0], pct(0.25), pct(0.5), pct(0.75), pct(0.9), sorted[len(sorted)-1])
	avgRects := float64(totalRects) / float64(len(g.rects))
	fmt.Printf("  rects per update: average %.1f\n", avgRects)

	// How the same updates group at different quiet thresholds. Fewer, larger
	// groups means the threshold is seeing frames rather than fragments of them.
	for _, q := range []float64{2, 5, 10, 16, 30, 50} {
		groups := 1
		for _, gap := range gaps {
			if gap >= q {
				groups++
			}
		}
		fmt.Printf("  quiet >= %.0f ms: %d groups, %.1f updates per group\n",
			q, groups, float64(len(g.times))/float64(groups))
	}
}
