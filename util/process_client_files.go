package util

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// codePrefix matches leading account code patterns like "6001 ", "40500-000 ", "4009.1 ".
var codePrefix = regexp.MustCompile(`^\s*\d[\d.\-]*\s+`)

// stripCodePrefix removes the leading numeric code from an account name,
// returning only the descriptive name portion.
func stripCodePrefix(s string) string {
	return strings.TrimSpace(codePrefix.ReplaceAllString(s, ""))
}

var monthSubstrings = []string{
	"jan", "feb", "mar", "apr", "may", "jun",
	"jul", "aug", "sep", "oct", "nov", "dec",
}


// LoadCategoryNamesFromXLSX reads all tabs of categories_index.xlsx and returns
// a map of lowercase code → threshold value (col B). Entries with no threshold value default to 0.
func LoadCategoryNamesFromXLSX(xlsxPath string) (map[string]float64, error) {
	f, err := excelize.OpenFile(xlsxPath)
	if err != nil {
		return nil, fmt.Errorf("open categories xlsx: %w", err)
	}
	defer f.Close()

	categories := map[string]float64{}
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		for i, row := range rows {
			if i == 0 { // skip header row
				continue
			}
			if len(row) == 0 || strings.TrimSpace(row[0]) == "" {
				continue
			}
			// stripped := strings.ToLower(stripCodePrefix(row[0]))
			name := strings.ToLower(strings.TrimSpace(row[0]))
			if name == "" {
				continue
			}
			var threshold float64
			if len(row) >= 2 && strings.TrimSpace(row[1]) != "" {
				if v, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64); err == nil {
					threshold = v
				}
			}
			categories[name] = threshold
		}
	}
	return categories, nil
}

// DownloadAndProcess downloads each xlsx attachment for the task, runs ProcessFinancials
// on it, and returns a map of filename → processed bytes.
func DownloadAndProcess(ctx context.Context, httpClient *http.Client, attachments []Attachment, categoryNames map[string]float64) (map[string][]byte, error) {
	results := map[string][]byte{}
	for _, a := range attachments {
		if !strings.HasSuffix(strings.ToLower(a.FileName), ".xlsx") {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.DownloadURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build request %s: %w", a.FileName, err)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", a.FileName, err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read body %s: %w", a.FileName, err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download %s: unexpected status %d", a.FileName, resp.StatusCode)
		}
		processed, err := ProcessFinancials(data, categoryNames)
		if err != nil {
			return nil, fmt.Errorf("process %s: %w", a.FileName, err)
		}
		results[a.FileName] = processed
	}
	return results, nil
}

// ProcessFinancials opens an xlsx file, finds the Income Statement / Profit & Loss sheet,
// and for each row whose column A matches a category in categoryNames:
//   - highlights empty month cells yellow (partial missing data)
//   - highlights the last month cell red if it falls outside avg±threshold
//
// Rows not matching any category are skipped entirely.
func ProcessFinancials(data []byte, categoryNames map[string]float64) ([]byte, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open xlsx: %w", err)
	}
	defer f.Close()

	sheetName, err := findIncomeSheet(f)
	if err != nil {
		return nil, err
	}

	dimension, err := f.GetSheetDimension(sheetName)
	if err != nil {
		return nil, fmt.Errorf("get sheet dimension: %w", err)
	}
	_, maxRow, maxCol, err := parseDimension(dimension)
	if err != nil {
		return nil, fmt.Errorf("parse dimension %q: %w", dimension, err)
	}

	// Read all cells by actual Excel coordinates.
	grid := make([][]string, maxRow)
	for row := 1; row <= maxRow; row++ {
		grid[row-1] = make([]string, maxCol)
		for col := 1; col <= maxCol; col++ {
			cellName, _ := excelize.CoordinatesToCellName(col, row)
			val, _ := f.GetCellValue(sheetName, cellName)
			grid[row-1][col-1] = val
		}
	}

	headerExcelRow, monthCols := findHeaderAndMonthCols(grid, maxRow)
	if headerExcelRow == -1 || len(monthCols) == 0 {
		return nil, fmt.Errorf("could not find month column headers in first 10 rows")
	}

	yellowStyleCache := map[int]int{}
	redStyleCache := map[int]int{}

	for row := headerExcelRow + 1; row <= maxRow; row++ {
		cells := grid[row-1]
		if len(cells) == 0 {
			continue
		}

		colA := strings.TrimSpace(cells[0])
		if strings.Contains(strings.ToLower(colA), "total") {
			continue
		}

		// Filter by categories_index.xlsx: match full column A value.
		threshold, matched := categoryNames[strings.ToLower(colA)]
		// stripped := strings.ToLower(stripCodePrefix(colA)); threshold, matched = categoryNames[stripped]
		if len(categoryNames) > 0 && !matched {
			continue
		}

		if isPartiallyMissing(cells, monthCols) {
			if err := highlightMissingCells(f, sheetName, row, cells, monthCols, yellowStyleCache); err != nil {
				return nil, fmt.Errorf("highlight missing cells row %d: %w", row, err)
			}
		}

		if threshold > 0 {
			if err := highlightMethodOutliers(f, sheetName, row, cells, monthCols, threshold, redStyleCache); err != nil {
				return nil, fmt.Errorf("highlight threshold outliers row %d: %w", row, err)
			}
		}
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("write xlsx: %w", err)
	}
	return buf.Bytes(), nil
}


// parseDimension parses a range like "A1:N74" and returns (minRow, maxRow, maxCol, error).
func parseDimension(dim string) (minRow, maxRow, maxCol int, err error) {
	parts := strings.Split(dim, ":")
	if len(parts) != 2 {
		return 0, 0, 0, fmt.Errorf("unexpected format")
	}
	_, minRow, err = excelize.CellNameToCoordinates(parts[0])
	if err != nil {
		return
	}
	maxCol, maxRow, err = excelize.CellNameToCoordinates(parts[1])
	return
}

// findHeaderAndMonthCols scans the first 10 Excel rows to find the header row.
// Returns the 1-based Excel row number and 0-based column indices of month columns.
// Column A (index 0) and any column containing "total" are always excluded.
func findHeaderAndMonthCols(grid [][]string, maxRow int) (headerExcelRow int, monthCols []int) {
	limit := 10
	if maxRow < limit {
		limit = maxRow
	}
	for row := 1; row <= limit; row++ {
		var cols []int
		for j, cell := range grid[row-1] {
			if j == 0 {
				continue
			}
			lower := strings.ToLower(cell)
			if strings.Contains(lower, "total") {
				continue
			}
			for _, month := range monthSubstrings {
				if strings.Contains(lower, month) {
					cols = append(cols, j)
					break
				}
			}
		}
		if len(cols) > 0 {
			return row, cols
		}
	}
	return -1, nil
}

// findIncomeSheet returns the name of the first sheet containing "income", "profit", or "loss".
func findIncomeSheet(f *excelize.File) (string, error) {
	for _, name := range f.GetSheetList() {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "income") || strings.Contains(lower, "profit") || strings.Contains(lower, "loss") {
			return name, nil
		}
	}
	return "", fmt.Errorf("no income statement / profit & loss sheet found")
}

// isPartiallyMissing returns true if the row has at least one filled and one empty month cell.
func isPartiallyMissing(row []string, monthCols []int) bool {
	filled, empty := 0, 0
	for _, col := range monthCols {
		if col >= len(row) || strings.TrimSpace(row[col]) == "" {
			empty++
		} else {
			filled++
		}
	}
	return filled > 0 && empty > 0
}

// highlightMissingCells applies yellow fill to empty month cells in the row.
func highlightMissingCells(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, styleCache map[int]int) error {
	rowBorder := resolveRowBorder(f, sheet, rowNum, monthCols)
	for _, col := range monthCols {
		if col >= len(cells) || strings.TrimSpace(cells[col]) != "" {
			continue
		}
		cellName, err := excelize.CoordinatesToCellName(col+1, rowNum)
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
		if err := f.SetCellStyle(sheet, cellName, cellName, mergedID); err != nil {
			return err
		}
	}
	return nil
}

// highlightMethodOutliers calculates the average of all month cells in the row, then checks
// only the last non-empty month cell. If its value is greater than avg+threshold or less than
// avg-threshold, it is tinted red.
func highlightMethodOutliers(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, threshold float64, styleCache map[int]int) error {
	type colVal struct {
		col int
		val float64
	}
	var vals []colVal
	for _, col := range monthCols {
		if col >= len(cells) || strings.TrimSpace(cells[col]) == "" {
			continue
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(cells[col]), ",", ""), 64)
		if err != nil {
			continue
		}
		vals = append(vals, colVal{col, v})
	}
	if len(vals) == 0 {
		return nil
	}

	var sum float64
	for _, cv := range vals {
		sum += cv.val
	}
	avg := sum / float64(len(vals))
	rowBorder := resolveRowBorder(f, sheet, rowNum, monthCols)

	// Only check the last non-empty month cell (rightmost, immediately left of total).
	last := vals[len(vals)-1]
	if last.val <= avg+threshold && last.val >= avg-threshold {
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


// resolveRowBorder finds an explicit border from a filled cell in the row, or falls back to thin black.
func resolveRowBorder(f *excelize.File, sheet string, rowNum int, monthCols []int) []excelize.Border {
	for _, col := range monthCols {
		cellName, err := excelize.CoordinatesToCellName(col+1, rowNum)
		if err != nil {
			continue
		}
		styleID, err := f.GetCellStyle(sheet, cellName)
		if err != nil {
			continue
		}
		style, err := f.GetStyle(styleID)
		if err != nil {
			continue
		}
		if len(style.Border) > 0 {
			return style.Border
		}
	}
	return []excelize.Border{
		{Type: "left", Color: "000000", Style: 1},
		{Type: "right", Color: "000000", Style: 1},
		{Type: "top", Color: "000000", Style: 1},
		{Type: "bottom", Color: "000000", Style: 1},
	}
}
