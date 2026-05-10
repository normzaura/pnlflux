package util

import (
	"math"
	"sort"
)

// ProcessStats holds counts of flagged cells produced during processing.
type ProcessStats struct {
	Missing      int // red-tinted last month cells (empty)
	Flux         int // yellow-tinted last month cells (fluctuation)
	Inconsistent int // yellow-tinted balance sheet cells (TB Match mismatch)
}

// computeThresholdStats derives threshold and supporting statistics from a slice
// of normalized monthly values using the formula:
//
//	threshold = max(policyMin, min(k×stdDev, 0.5×maxDelta, percentile95(|Δ%|)))
//
// All returned percentages are in percentage-point units (same scale as pctDiff).
func computeThresholdStats(normVals []float64, k, policyMin float64) (threshold, stdDev, avgDelta, minVal, maxVal float64) {
	if len(normVals) == 0 {
		return 0, 0, 0, 0, 0
	}

	minVal = normVals[0]
	maxVal = normVals[0]
	for _, v := range normVals {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}

	// Month-over-month absolute percent changes.
	var deltas []float64
	for i := 1; i < len(normVals); i++ {
		prev := normVals[i-1]
		if prev != 0 {
			deltas = append(deltas, math.Abs(normVals[i]-prev)/math.Abs(prev)*100)
		}
	}
	if len(deltas) == 0 {
		return 0, 0, 0, minVal, maxVal
	}

	var sum float64
	for _, d := range deltas {
		sum += d
	}
	avgDelta = sum / float64(len(deltas))

	var varSum float64
	for _, d := range deltas {
		diff := d - avgDelta
		varSum += diff * diff
	}
	stdDev = math.Sqrt(varSum / float64(len(deltas)))

	sorted := make([]float64, len(deltas))
	copy(sorted, deltas)
	sort.Float64s(sorted)

	maxDelta := sorted[len(sorted)-1]
	idx := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	pct95 := sorted[idx]

	inner := math.Min(k*stdDev, math.Min(0.5*maxDelta, pct95))
	threshold = math.Max(policyMin, inner)
	return threshold, stdDev, avgDelta, minVal, maxVal
}
