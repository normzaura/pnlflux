package util

import (
	"math"
	"sort"
	"strings"
	"time"
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
// All values are in decimal format (0.15 = 15%), matching the DB storage format.
func computeThresholdStats(normVals []float64, k, policyMin float64) (threshold, sigmaHat, avgDelta float64) {
	if len(normVals) == 0 {
		return 0, 0, 0
	}

	// Month-over-month absolute percent changes.
	var deltas []float64
	for i := 1; i < len(normVals); i++ {
		prev := normVals[i-1]
		if prev != 0 {
			deltas = append(deltas, math.Abs(normVals[i]-prev)/math.Abs(prev))
		}
	}
	if len(deltas) == 0 {
		return 0, 0, 0
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

	threshold = math.Round(math.Max(policyMin, k*sigmaHat)*100) / 100
	return threshold, sigmaHat, avgDelta
}

// computeHistoryMetrics derives avg_absdelta and flag_rate from the full stored history
// for a given company. History entries are sorted chronologically before computing
// month-over-month absolute % changes.
//
//   - avg_absdelta: mean of all |Δ%| across consecutive months
//   - flag_rate:    fraction of those deltas that exceed threshold (0 when threshold == 0)
func computeHistoryMetrics(entries []HistoryEntry, companyName string, threshold float64) (avgAbsDelta, flagRate float64) {
	var hist []map[string]float64
	for _, e := range entries {
		if strings.EqualFold(e.Company.Name, companyName) {
			hist = e.History
			break
		}
	}
	if len(hist) < 2 {
		return 0, 0
	}

	type mv struct {
		t   time.Time
		val float64
	}
	var points []mv
	for _, h := range hist {
		for monthStr, val := range h {
			t, err := time.Parse("01-2006", monthStr)
			if err != nil {
				continue
			}
			points = append(points, mv{t, val})
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].t.Before(points[j].t) })
	if len(points) < 2 {
		return 0, 0
	}

	var deltas []float64
	for i := 1; i < len(points); i++ {
		prev := points[i-1].val
		if prev == 0 {
			continue
		}
		deltas = append(deltas, math.Abs(points[i].val-prev)/math.Abs(prev))
	}
	if len(deltas) == 0 {
		return 0, 0
	}

	var sum float64
	var flagged int
	for _, d := range deltas {
		sum += d
		if threshold > 0 && d > threshold {
			flagged++
		}
	}
	avgAbsDelta = math.Round(sum/float64(len(deltas))*100) / 100
	if threshold > 0 {
		flagRate = math.Round(float64(flagged)/float64(len(deltas))*100) / 100
	}
	return avgAbsDelta, flagRate
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
