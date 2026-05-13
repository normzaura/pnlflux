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
//	σ̂        = 1.4826 × MAD(|Δ%|)
//	threshold = max(policyMin, k × σ̂)
//
// All returned percentages are in percentage-point units (same scale as pctDiff).
func computeThresholdStats(normVals []float64, k, policyMin float64) (threshold, sigmaHat, avgDelta, minVal, maxVal float64) {
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

	sorted := make([]float64, len(deltas))
	copy(sorted, deltas)
	sort.Float64s(sorted)

	medianDeltas := median(sorted)
	absDevs := make([]float64, len(sorted))
	for i, d := range sorted {
		absDevs[i] = math.Abs(d - medianDeltas)
	}
	sort.Float64s(absDevs)
	sigmaHat = 1.4826 * median(absDevs)

	threshold = math.Max(policyMin, k*sigmaHat)
	return threshold, sigmaHat, avgDelta, minVal, maxVal
}

// median returns the median of a pre-sorted slice.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
