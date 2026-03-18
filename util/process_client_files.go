package util

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

var monthSubstrings = []string{
	"jan", "feb", "mar", "apr", "may", "jun",
	"jul", "aug", "sep", "oct", "nov", "dec",
}

// Category holds a matched account name and its MAD outlier threshold.
type Category struct {
	Name      string
	Threshold float64
}

// DownloadAndProcess downloads each xlsx attachment for the task, runs ProcessFinancials
// on it, and returns a map of filename → processed bytes.
func DownloadAndProcess(ctx context.Context, httpClient *http.Client, attachments []Attachment, cats []Category) (map[string][]byte, error) {
	results := map[string][]byte{}
	for _, a := range attachments {
		if !strings.HasSuffix(strings.ToLower(a.FileName), ".xlsx") {
			continue
		}
		data, err := DownloadFinancial(ctx, httpClient, a.DownloadURL)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", a.FileName, err)
		}
		processed, err := ProcessFinancials(data, cats)
		if err != nil {
			return nil, fmt.Errorf("process %s: %w", a.FileName, err)
		}
		results[a.FileName] = processed
	}
	return results, nil
}

// LoadCategories parses categories_index.csv into a slice of Category.
// Row 1 is the header and is skipped. Each non-empty cell in subsequent rows
// must be formatted as "account name:threshold".
func LoadCategories(csvPath string) ([]Category, error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("open categories csv: %w", err)
	}
	defer f.Close()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read categories csv: %w", err)
	}
	if len(rows) < 2 {
		return nil, nil
	}

	var cats []Category
	for _, row := range rows[1:] { // skip header row
		for _, cell := range row {
			cell = strings.TrimSpace(cell)
			if cell == "" {
				continue
			}
			parts := strings.SplitN(cell, ":", 2)
			name := strings.ToLower(strings.TrimSpace(parts[0]))
			threshold := 3.5 // sensible default
			if len(parts) == 2 {
				if t, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil {
					threshold = t
				}
			}
			cats = append(cats, Category{Name: name, Threshold: threshold})
		}
	}
	return cats, nil
}

// matchCategory returns the Category whose name is contained within colA (case-insensitive).
// Returns nil if no match.
func matchCategory(colA string, cats []Category) *Category {
	lower := strings.ToLower(colA)
	for i := range cats {
		if strings.Contains(lower, cats[i].Name) {
			return &cats[i]
		}
	}
	return nil
}

// ProcessFinancials opens an xlsx file, finds the Income Statement / Profit & Loss sheet,
// and for each row whose column A matches a category in cats:
//   - highlights empty month cells yellow (partial missing data)
//   - highlights month cells that are MAD outliers orange
//
// Rows not matching any category are skipped entirely.
func ProcessFinancials(data []byte, cats []Category) ([]byte, error) {
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
	orangeStyleCache := map[int]int{}

	for row := headerExcelRow + 1; row <= maxRow; row++ {
		cells := grid[row-1]
		if len(cells) == 0 {
			continue
		}

		colA := strings.TrimSpace(cells[0])
		if strings.Contains(strings.ToLower(colA), "total") {
			continue
		}

		cat := matchCategory(colA, cats)
		if cat == nil {
			continue // row does not match any tracked category
		}

		if isPartiallyMissing(cells, monthCols) {
			if err := highlightMissingCells(f, sheetName, row, cells, monthCols, yellowStyleCache); err != nil {
				return nil, fmt.Errorf("highlight missing cells row %d: %w", row, err)
			}
		}

		if err := highlightOutliers(f, sheetName, row, cells, monthCols, cat.Threshold, orangeStyleCache); err != nil {
			return nil, fmt.Errorf("highlight outliers row %d: %w", row, err)
		}
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("write xlsx: %w", err)
	}
	return buf.Bytes(), nil
}

// highlightOutliers applies an orange fill to month cells whose value is a MAD outlier.
func highlightOutliers(f *excelize.File, sheet string, rowNum int, cells []string, monthCols []int, threshold float64, styleCache map[int]int) error {
	// collect (colIndex, value) pairs for non-empty month cells
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
	if len(vals) < 3 {
		return nil // not enough data points for meaningful MAD
	}

	floats := make([]float64, len(vals))
	for i, cv := range vals {
		floats[i] = cv.val
	}
	med := median(floats)
	mad := computeMAD(floats, med)
	if mad == 0 {
		return nil // all values identical, no outliers possible
	}

	rowBorder := resolveRowBorder(f, sheet, rowNum, monthCols)

	for _, cv := range vals {
		if math.Abs(cv.val-med) <= threshold*mad {
			continue
		}
		cellName, err := excelize.CoordinatesToCellName(cv.col+1, rowNum)
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
				Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FFA500"}, Pattern: 1},
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

func median(vals []float64) float64 {
	s := make([]float64, len(vals))
	copy(s, vals)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 0 {
		return (s[n/2-1] + s[n/2]) / 2
	}
	return s[n/2]
}

func computeMAD(vals []float64, med float64) float64 {
	devs := make([]float64, len(vals))
	for i, v := range vals {
		devs[i] = math.Abs(v - med)
	}
	return median(devs)
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
