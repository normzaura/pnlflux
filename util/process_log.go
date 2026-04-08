package util

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxLogAgeDays = 7

// ProcessLogger writes detailed per-row calculation logs to both a local dated
// file and an in-memory buffer. The buffer contents can be retrieved via Bytes()
// for upload to S3.
type ProcessLogger struct {
	w   *bufio.Writer
	f   *os.File
	buf *bytes.Buffer
}

// NewProcessLogger creates a logger that always writes to an in-memory buffer
// (for S3 upload). When TEST=true it additionally writes to a local dated file
// with 7-day FIFO retention for local debugging.
func NewProcessLogger(inputFileName string) (*ProcessLogger, error) {
	buf := &bytes.Buffer{}
	var writer io.Writer = buf

	if strings.EqualFold(os.Getenv("TEST"), "true") {
		if err := os.MkdirAll("logs", 0755); err != nil {
			return nil, fmt.Errorf("create logs dir: %w", err)
		}
		if err := pruneOldLogs("logs", maxLogAgeDays); err != nil {
			return nil, fmt.Errorf("prune logs: %w", err)
		}
		base := strings.TrimSuffix(filepath.Base(inputFileName), filepath.Ext(inputFileName))
		date := time.Now().Format("2006-01-02")
		path := filepath.Join("logs", fmt.Sprintf("%s_%s.log", base, date))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		writer = io.MultiWriter(file, buf)
		l := &ProcessLogger{w: bufio.NewWriter(writer), f: file, buf: buf}
		fmt.Fprintf(l.w, "=== %s processed at %s ===\n\n", inputFileName, time.Now().Format("2006-01-02 15:04:05"))
		return l, nil
	}

	l := &ProcessLogger{w: bufio.NewWriter(writer), buf: buf}
	fmt.Fprintf(l.w, "=== %s processed at %s ===\n\n", inputFileName, time.Now().Format("2006-01-02 15:04:05"))
	return l, nil
}

// Bytes returns the full log content written during this session, suitable for upload to S3.
func (l *ProcessLogger) Bytes() []byte {
	l.w.Flush()
	return l.buf.Bytes()
}

// pruneOldLogs deletes log files in dir whose embedded date is older than maxDays.
// Expected filename format: <name>_<YYYY-MM-DD>.log
func pruneOldLogs(dir string, maxDays int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -maxDays)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".log")
		parts := strings.Split(name, "_")
		if len(parts) == 0 {
			continue
		}
		datePart := parts[len(parts)-1]
		t, err := time.Parse("2006-01-02", datePart)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// Close flushes buffered writes and closes the underlying file (if any).
func (l *ProcessLogger) Close() {
	l.w.Flush()
	if l.f != nil {
		l.f.Close()
	}
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

// LogReconcile records a TB Match reconciliation result for a category.
func (l *ProcessLogger) LogReconcile(category string, tbVal, bsVal float64, match bool) {
	if match {
		fmt.Fprintf(l.w, "[RECONCILE] OK       %-60s  TB=%.2f  BS=%.2f\n", category, tbVal, bsVal)
	} else {
		fmt.Fprintf(l.w, "[RECONCILE] MISMATCH %-60s  TB=%.2f  BS=%.2f — discrepancy\n", category, tbVal, bsVal)
	}
}

// LogSection writes a blank-line-padded section header.
func (l *ProcessLogger) LogSection(title string) {
	fmt.Fprintf(l.w, "\n--- %s ---\n", title)
}
