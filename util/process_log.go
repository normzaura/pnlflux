package util

import (
	"bufio"
	"bytes"
	"fmt"
	"time"
)

// ProcessLogger writes detailed per-row calculation logs to an in-memory buffer
// for upload to S3.
type ProcessLogger struct {
	w   *bufio.Writer
	buf *bytes.Buffer
}

// NewProcessLogger creates a logger that writes to an in-memory buffer.
func NewProcessLogger(inputFileName string) (*ProcessLogger, error) {
	buf := &bytes.Buffer{}
	l := &ProcessLogger{w: bufio.NewWriter(buf), buf: buf}
	fmt.Fprintf(l.w, "=== %s processed at %s ===\n\n", inputFileName, time.Now().Format("2006-01-02 15:04:05"))
	return l, nil
}

// Bytes returns the full log content written during this session, suitable for upload to S3.
func (l *ProcessLogger) Bytes() []byte {
	l.w.Flush()
	return l.buf.Bytes()
}

// Close flushes buffered writes.
func (l *ProcessLogger) Close() {
	l.w.Flush()
}

// LogMatch records that a row's column A matched a category.
func (l *ProcessLogger) LogMatch(rowNum int, colA string) {
	fmt.Fprintf(l.w, "[ROW %d] MATCH: %s\n", rowNum, colA)
}

// LogAllEmpty records that all month cells in a row were empty and the row was skipped.
func (l *ProcessLogger) LogAllEmpty(rowNum int, colA string) {
	fmt.Fprintf(l.w, "[ROW %d]   all month cells empty — skipped\n", rowNum)
}

// LogEmptyLastMonth records that the last month cell is empty and will be tinted red.
func (l *ProcessLogger) LogEmptyLastMonth(rowNum int, colA string) {
	fmt.Fprintf(l.w, "[ROW %d]   last month cell is empty — tinted red\n", rowNum)
}

// LogFluctuation records the full fluctuation calculation for a matched row.
func (l *ProcessLogger) LogFluctuation(
	rowNum int,
	colA string,
	monthHeaders []string,
	rawVals []float64,
	normVals []float64,
	normalized bool,
	avg float64,
	lastRaw float64,
	lastNorm float64,
	pctDiff float64,
	threshold float64,
	flagged bool,
) {
	fmt.Fprintf(l.w, "[ROW %d]   fluctuation check (threshold %.1f%%):\n", rowNum, threshold)
	for i, v := range rawVals {
		header := ""
		if i < len(monthHeaders) {
			header = monthHeaders[i]
		}
		if normalized && i < len(normVals) {
			fmt.Fprintf(l.w, "             %s: raw=%.2f  normalized=%.6f\n", header, v, normVals[i])
		} else {
			fmt.Fprintf(l.w, "             %s: %.2f\n", header, v)
		}
	}
	if normalized {
		fmt.Fprintf(l.w, "           average (normalized, excl. last month): %.6f\n", avg)
		fmt.Fprintf(l.w, "           last month: raw=%.2f  normalized=%.6f\n", lastRaw, lastNorm)
		fmt.Fprintf(l.w, "           %% diff: |%.6f - %.6f| / |%.6f| * 100 = %.4f%%\n", lastNorm, avg, avg, pctDiff)
	} else {
		fmt.Fprintf(l.w, "           average (excl. last month): %.2f\n", avg)
		fmt.Fprintf(l.w, "           last month: %.2f\n", lastRaw)
		fmt.Fprintf(l.w, "           %% diff: |%.2f - %.2f| / |%.2f| * 100 = %.4f%%\n", lastRaw, avg, avg, pctDiff)
	}
	if flagged {
		fmt.Fprintf(l.w, "           -> FLAGGED — cell tinted yellow\n")
	} else {
		fmt.Fprintf(l.w, "           -> OK — within threshold\n")
	}
}

// LogNoThreshold records that a matched row has no threshold set and fluctuation was not checked.
func (l *ProcessLogger) LogNoThreshold(rowNum int, colA string) {
	fmt.Fprintf(l.w, "[ROW %d]   no threshold set — fluctuation check skipped\n", rowNum)
}

// LogNoK records that a matched row has no k set for this company and threshold cannot be computed.
func (l *ProcessLogger) LogNoK(rowNum int, colA string) {
	fmt.Fprintf(l.w, "[ROW %d]   NO K SET for %q — cannot compute threshold, last month tinted blue\n", rowNum, colA)
}

// LogReconcile records a TB Match reconciliation result for a category.
func (l *ProcessLogger) LogReconcile(category string, tbVal, bsVal float64, match bool) {
	if match {
		fmt.Fprintf(l.w, "[RECONCILE] OK       %-60s  TB=%.2f  BS=%.2f\n", category, tbVal, bsVal)
	} else {
		fmt.Fprintf(l.w, "[RECONCILE] MISMATCH %-60s  TB=%.2f  BS=%.2f — discrepancy\n", category, tbVal, bsVal)
	}
}

// LogExcluded records that a special term row exists in public.excluded and was skipped.
func (l *ProcessLogger) LogExcluded(rowNum int, colA string) {
	fmt.Fprintf(l.w, "[ROW %d] EXCLUDED: %s — found in public.excluded, skipped\n", rowNum, colA)
}

// LogNoPolicy records that a matched row has no policy_min_threshold and was skipped.
func (l *ProcessLogger) LogNoPolicy(rowNum int, colA string) {
	fmt.Fprintf(l.w, "[ROW %d]   no policy_min_threshold set — skipped\n", rowNum)
}

// LogThreshold records the computed threshold and whether k×σ̂ or the policyMin floor was used.
func (l *ProcessLogger) LogThreshold(rowNum int, k, sigmaHat, kResult, policyMin, threshold float64) {
	source := "k×σ̂"
	if kResult < policyMin {
		source = "policyMin floor"
	}
	fmt.Fprintf(l.w, "[ROW %d]   threshold: k=%.4f  σ̂=%.4f  k×σ̂=%.4f  policyMin=%.4f  → %.2f%% (%s)\n",
		rowNum, k, sigmaHat, kResult, policyMin, threshold*100, source)
}

// LogDollarSuppressed records that a flagged row was suppressed by the dollar floor and tinted green.
func (l *ProcessLogger) LogDollarSuppressed(rowNum int, dollarDelta, dollarFloor float64) {
	fmt.Fprintf(l.w, "[ROW %d]   DOLLAR SUPPRESSED: delta=$%.2f below floor=$%.2f — tinted green\n", rowNum, dollarDelta, dollarFloor)
}

// LogFluctuationStatus records the final fluctuation outcome for the row.
func (l *ProcessLogger) LogFluctuationStatus(rowNum int, status string) {
	fmt.Fprintf(l.w, "[ROW %d]   status: %s\n", rowNum, status)
}

// LogClaudeSkipped records that the Claude agent was not called and why.
func (l *ProcessLogger) LogClaudeSkipped(rowNum int, reason string) {
	fmt.Fprintf(l.w, "[ROW %d]   claude agent: skipped (%s)\n", rowNum, reason)
}

// LogClaudeResult records the Claude agent's recommendation.
func (l *ProcessLogger) LogClaudeResult(rowNum int, agentK float64, justification string) {
	fmt.Fprintf(l.w, "[ROW %d]   claude agent: agent_k_threshold=%.4f  justification=%q\n", rowNum, agentK, justification)
}

// LogClaudeError records that the Claude agent call failed.
func (l *ProcessLogger) LogClaudeError(rowNum int, err error) {
	fmt.Fprintf(l.w, "[ROW %d]   claude agent: ERROR — %v\n", rowNum, err)
}

// LogSection writes a blank-line-padded section header.
func (l *ProcessLogger) LogSection(title string) {
	fmt.Fprintf(l.w, "\n--- %s ---\n", title)
}
