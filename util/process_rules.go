package util

import (
	"math"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// highlightEmptyCell tints the last month cell red if it is empty.
func highlightEmptyCell(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, styleCache map[int]int) error {
	if len(monthCols) == 0 {
		return nil
	}
	lastCol := monthCols[len(monthCols)-1]
	if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) != "" {
		return nil
	}
	cellName, err := excelize.CoordinatesToCellName(lastCol+1, rowNum)
	if err != nil {
		return err
	}
	existingID, err := f.GetCellStyle(sheet, cellName)
	if err != nil {
		return err
	}
	mergedID, ok := styleCache[existingID]
	if !ok {
		existing, err := f.GetStyle(existingID)
		if err != nil {
			return err
		}
		rowBorder := resolveRowBorder(f, sheet, rowNum, monthCols)
		border := existing.Border
		if len(border) == 0 {
			border = rowBorder
		}
		merged, err := f.NewStyle(&excelize.Style{
			Border:    border,
			Alignment: existing.Alignment,
			Font:      existing.Font,
			Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FF0000"}, Pattern: 1},
		})
		if err != nil {
			return err
		}
		styleCache[existingID] = merged
		mergedID = merged
	}
	return f.SetCellStyle(sheet, cellName, cellName, mergedID)
}

// detectFluctuation calculates the average of all previous month cells (excluding the last),
// then checks the last month cell. If its value differs from that average by more than
// threshold%, it is tinted yellow.
//
// When divisorCells is non-nil (code-6 rows), each month value is divided by the
// corresponding total income cell before comparison, normalizing the expense as a
// fraction of revenue.
func detectFluctuation(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, threshold float64, divisorCells []string, styleCache map[int]int) error {
	if len(monthCols) == 0 {
		return nil
	}

	// Only evaluate the last month column. If it's empty, skip — it's already flagged by highlightEmptyCell.
	lastCol := monthCols[len(monthCols)-1]
	if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) == "" {
		return nil
	}
	lastVal, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(cells[lastCol]), ",", ""), 64)
	if err != nil {
		return nil
	}

	// Normalize by total income when divisorCells is provided.
	normalize := func(val float64, col int) (float64, bool) {
		if divisorCells == nil {
			return val, true
		}
		if col >= len(divisorCells) || strings.TrimSpace(divisorCells[col]) == "" {
			return 0, false
		}
		d, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(divisorCells[col]), ",", ""), 64)
		if err != nil || d == 0 {
			return 0, false
		}
		return val / d, true
	}

	normLast, ok := normalize(lastVal, lastCol)
	if !ok {
		return nil
	}

	// Build average from all previous month columns that have values.
	type colVal struct {
		col int
		val float64
	}
	var previous []colVal
	for _, col := range monthCols[:len(monthCols)-1] {
		if col >= len(cells) || strings.TrimSpace(cells[col]) == "" {
			continue
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(cells[col]), ",", ""), 64)
		if err != nil {
			continue
		}
		nv, ok := normalize(v, col)
		if !ok {
			continue
		}
		previous = append(previous, colVal{col, nv})
	}
	if len(previous) == 0 {
		return nil
	}

	last := colVal{lastCol, normLast}

	var sum float64
	for _, cv := range previous {
		sum += cv.val
	}
	avg := sum / float64(len(previous))

	if avg == 0 {
		return nil
	}

	pctDiff := math.Abs(last.val-avg) / math.Abs(avg) * 100
	rowBorder := resolveRowBorder(f, sheet, rowNum, monthCols)

	if pctDiff <= threshold {
		return nil
	}

	cellName, err := excelize.CoordinatesToCellName(last.col+1, rowNum)
	if err != nil {
		return err
	}
	existingID, err := f.GetCellStyle(sheet, cellName)
	if err != nil {
		return err
	}
	mergedID, ok := styleCache[existingID]
	if !ok {
		existing, err := f.GetStyle(existingID)
		if err != nil {
			return err
		}
		border := existing.Border
		if len(border) == 0 {
			border = rowBorder
		}
		merged, err := f.NewStyle(&excelize.Style{
			Border:    border,
			Alignment: existing.Alignment,
			Font:      existing.Font,
			Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FFFF00"}, Pattern: 1},
		})
		if err != nil {
			return err
		}
		styleCache[existingID] = merged
		mergedID = merged
	}
	return f.SetCellStyle(sheet, cellName, cellName, mergedID)
}
