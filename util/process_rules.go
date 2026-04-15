package util

import (
	"math"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// parseAmount parses a currency string into a float64.
// Strips leading "$", commas, and handles parentheses notation for negatives
// (e.g. "(1,234.56)" → -1234.56).
func parseAmount(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, ",", "")
	negative := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	if negative {
		s = s[1 : len(s)-1]
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	if negative {
		v = -v
	}
	return v, nil
}

// highlightEmptyCell tints the last month cell red if it is empty.
// Returns true if the cell was tinted.
func highlightEmptyCell(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, styleCache map[int]int) (bool, error) {
	if len(monthCols) == 0 {
		return false, nil
	}
	lastCol := monthCols[len(monthCols)-1]
	if lastCol >= len(cells) {
		return false, nil
	}
	v := strings.TrimSpace(cells[lastCol])
	if v != "" && v != "0" && v != "0.00" {
		return false, nil
	}
	cellName, err := excelize.CoordinatesToCellName(lastCol+1, rowNum)
	if err != nil {
		return false, err
	}
	existingID, err := f.GetCellStyle(sheet, cellName)
	if err != nil {
		return false, err
	}
	mergedID, ok := styleCache[existingID]
	if !ok {
		existing, err := f.GetStyle(existingID)
		if err != nil {
			return false, err
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
			return false, err
		}
		styleCache[existingID] = merged
		mergedID = merged
	}
	return true, f.SetCellStyle(sheet, cellName, cellName, mergedID)
}

// tintGreenLastMonth applies a green fill to the last month cell of the row.
// Used for matched P&L rows whose last month cell has a value and passed all checks.
func tintGreenLastMonth(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, styleCache map[int]int) error {
	if len(monthCols) == 0 {
		return nil
	}
	lastCol := monthCols[len(monthCols)-1]
	if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) == "" {
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
		merged, err := f.NewStyle(&excelize.Style{
			Border:    existing.Border,
			Alignment: existing.Alignment,
			Font:      existing.Font,
			Fill:      excelize.Fill{Type: "pattern", Color: []string{"#00B050"}, Pattern: 1},
		})
		if err != nil {
			return err
		}
		styleCache[existingID] = merged
		mergedID = merged
	}
	return f.SetCellStyle(sheet, cellName, cellName, mergedID)
}

// tintOrangeLastMonth applies an orange fill to the last month cell of the row.
// Used for P&L rows that have values but no match in the category index.
func tintOrangeLastMonth(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, styleCache map[int]int) error {
	if len(monthCols) == 0 {
		return nil
	}
	lastCol := monthCols[len(monthCols)-1]
	if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) == "" {
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
		merged, err := f.NewStyle(&excelize.Style{
			Border:    existing.Border,
			Alignment: existing.Alignment,
			Font:      existing.Font,
			Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FF6600"}, Pattern: 1},
		})
		if err != nil {
			return err
		}
		styleCache[existingID] = merged
		mergedID = merged
	}
	return f.SetCellStyle(sheet, cellName, cellName, mergedID)
}

// normalizeByDivisor divides val by the parsed value of divisorCells[col].
// When divisorCells is nil the raw value is returned unchanged.
// Returns (0, false) if the divisor cell is missing, empty, zero, or unparseable.
func normalizeByDivisor(val float64, col int, divisorCells []string) (float64, bool) {
	if divisorCells == nil {
		return val, true
	}
	if col >= len(divisorCells) || strings.TrimSpace(divisorCells[col]) == "" {
		return 0, false
	}
	d, err := parseAmount(divisorCells[col])
	if err != nil || d == 0 {
		return 0, false
	}
	return val / d, true
}

// detectFluctuation calculates the average of all previous month cells (excluding the last),
// then checks the last month cell. If its value differs from that average by more than
// threshold%, it is tinted yellow.
//
// When divisorCells is non-nil (code-5 rows), each month value is normalized by the
// corresponding total income cell via normalizeByDivisor before comparison.
// Returns true if the cell was flagged and tinted.
func detectFluctuation(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, threshold float64, divisorCells []string, styleCache map[int]int) (bool, error) {
	if len(monthCols) == 0 {
		return false, nil
	}

	// Only evaluate the last month column. If it's empty, skip — it's already flagged by highlightEmptyCell.
	lastCol := monthCols[len(monthCols)-1]
	if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) == "" {
		return false, nil
	}
	lastVal, err := parseAmount(cells[lastCol])
	if err != nil || lastVal == 0 {
		return false, nil
	}

	normLast, ok := normalizeByDivisor(lastVal, lastCol, divisorCells)
	if !ok {
		return false, nil
	}

	// Build average from all previous month columns that have values.
	type colVal struct {
		col int
		val float64
	}
	var previous []colVal
	for _, col := range monthCols[:len(monthCols)-1] {
		var v float64
		if col < len(cells) && strings.TrimSpace(cells[col]) != "" {
			parsed, err := parseAmount(cells[col])
			if err != nil {
				parsed = 0
			}
			v = parsed
		}
		nv, ok := normalizeByDivisor(v, col, divisorCells)
		if !ok {
			continue
		}
		previous = append(previous, colVal{col, nv})
	}
	if len(previous) == 0 {
		return false, nil
	}

	last := colVal{lastCol, normLast}

	var sum float64
	for _, cv := range previous {
		sum += cv.val
	}
	avg := sum / float64(len(previous))

	if avg == 0 {
		return false, nil
	}

	pctDiff := math.Abs(last.val-avg) / math.Abs(avg) * 100
	rowBorder := resolveRowBorder(f, sheet, rowNum, monthCols)

	if pctDiff <= threshold {
		return false, nil
	}

	cellName, err := excelize.CoordinatesToCellName(last.col+1, rowNum)
	if err != nil {
		return false, err
	}
	existingID, err := f.GetCellStyle(sheet, cellName)
	if err != nil {
		return false, err
	}
	mergedID, ok := styleCache[existingID]
	if !ok {
		existing, err := f.GetStyle(existingID)
		if err != nil {
			return false, err
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
			return false, err
		}
		styleCache[existingID] = merged
		mergedID = merged
	}
	return true, f.SetCellStyle(sheet, cellName, cellName, mergedID)
}
