package util

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
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

	maxRow, maxCol, err := sheetDimensions(f, sheetName)
	if err != nil {
		return nil, fmt.Errorf("get sheet dimensions: %w", err)
	}

	// Read all cells by actual Excel coordinates.
	// Some xlsx exports (e.g. QuickBooks) store numeric values in the formula element
	// rather than the value element, so GetCellValue returns "". Fall back to
	// GetCellFormula and use it when it parses as a valid float.
	grid := make([][]string, maxRow)
	for row := 1; row <= maxRow; row++ {
		grid[row-1] = make([]string, maxCol)
		for col := 1; col <= maxCol; col++ {
			cellName, _ := excelize.CoordinatesToCellName(col, row)
			val, _ := f.GetCellValue(sheetName, cellName)
			if val == "" {
				if formula, _ := f.GetCellFormula(sheetName, cellName); formula != "" {
					if _, err := strconv.ParseFloat(strings.ReplaceAll(formula, ",", ""), 64); err == nil {
						val = formula
					}
				}
			}
			grid[row-1][col-1] = val
		}
	}

	rawRows, err := f.GetRows(sheetName)
	if err != nil {
		return nil, fmt.Errorf("get rows for header scan: %w", err)
	}
	headerExcelRow, monthCols := findHeaderAndMonthCols(rawRows, maxRow)
	if headerExcelRow == -1 || len(monthCols) == 0 {
		return nil, fmt.Errorf("could not find month column headers in first 10 rows")
	}

	// Pre-scan for the "Total Income" row to use as divisor for code-5 rows.
	// This row always appears above any code-5 item rows in the sheet.
	var totalCells []string
	for row := headerExcelRow + 1; row <= maxRow; row++ {
		if row-1 >= len(grid) {
			break
		}
		c := grid[row-1]
		if len(c) == 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(c[0]), "total income") {
			totalCells = c
			break
		}
	}

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

		fmt.Printf("[MATCH] %s\n", colA)

		// Determine if this is a code-6 row (expense account).
		// When it is, normalize each month's value against total income before comparison.
		itemCode := strings.SplitN(strings.TrimSpace(colA), " ", 2)[0]
		var divisorCells []string
		if strings.HasPrefix(itemCode, "5") && totalCells != nil {
			divisorCells = totalCells
		}

		// Check if last month cell is empty.
		if len(monthCols) > 0 {
			lastCol := monthCols[len(monthCols)-1]
			if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) == "" {
				fmt.Printf("  -> last month cell is empty\n")
			}
		}

		if err := highlightEmptyCell(f, sheetName, row, cells, monthCols, redStyleCache); err != nil {
			return nil, fmt.Errorf("highlight empty last month row %d: %w", row, err)
		}

		if threshold > 0 {
			// Compute and print threshold evaluation.
			if len(monthCols) > 0 {
				lastCol := monthCols[len(monthCols)-1]
				if lastCol < len(cells) && strings.TrimSpace(cells[lastCol]) != "" {
					lastVal, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(cells[lastCol]), ",", ""), 64)
					if err == nil {
						var sum float64
						var count int
						for _, col := range monthCols[:len(monthCols)-1] {
							if col >= len(cells) || strings.TrimSpace(cells[col]) == "" {
								continue
							}
							v, parseErr := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(cells[col]), ",", ""), 64)
							if parseErr != nil {
								continue
							}
							if divisorCells != nil {
								if col < len(divisorCells) && strings.TrimSpace(divisorCells[col]) != "" {
									d, dErr := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(divisorCells[col]), ",", ""), 64)
									if dErr == nil && d != 0 {
										v = v / d
									}
								}
							}
							sum += v
							count++
						}
						if count > 0 {
							avg := sum / float64(count)
							if avg != 0 {
								effectiveLast := lastVal
								if divisorCells != nil && lastCol < len(divisorCells) && strings.TrimSpace(divisorCells[lastCol]) != "" {
									d, dErr := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(divisorCells[lastCol]), ",", ""), 64)
									if dErr == nil && d != 0 {
										effectiveLast = lastVal / d
									}
								}
								pctDiff := math.Abs(effectiveLast-avg) / math.Abs(avg) * 100
								label := "ok"
								if pctDiff > threshold {
									label = "FLAGGED"
								}
								if divisorCells != nil {
									fmt.Printf("  -> (normalized by total) last %.4f vs avg %.4f (%.1f%% diff, threshold %.0f%%) — %s\n", effectiveLast, avg, pctDiff, threshold, label)
								} else {
									fmt.Printf("  -> last month %.2f vs avg %.2f (%.1f%% diff, threshold %.0f%%) — %s\n", lastVal, avg, pctDiff, threshold, label)
								}
							}
						}
					}
				}
			}

			if err := detectFluctuation(f, sheetName, row, cells, monthCols, threshold, divisorCells, redStyleCache); err != nil {
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
// Uses raw GetRows data (not GetCellValue) so merged cells don't bleed across columns.
func findHeaderAndMonthCols(rawRows [][]string, maxRow int) (headerExcelRow int, monthCols []int) {
	limit := 10
	if maxRow < limit {
		limit = maxRow
	}
	for row := 1; row <= limit; row++ {
		if row > len(rawRows) {
			break
		}
		var cols []int
		for j, cell := range rawRows[row-1] {
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

// sheetDimensions returns the max row and max column counts by reading all rows.
// Falls back from GetSheetDimension when the xlsx metadata is missing or empty.
func sheetDimensions(f *excelize.File, sheet string) (maxRow, maxCol int, err error) {
	dim, _ := f.GetSheetDimension(sheet)
	if dim != "" {
		_, maxRow, maxCol, err = parseDimension(dim)
		if err == nil {
			return
		}
	}
	rows, err := f.GetRows(sheet)
	if err != nil {
		return 0, 0, fmt.Errorf("get rows: %w", err)
	}
	maxRow = len(rows)
	for _, row := range rows {
		if len(row) > maxCol {
			maxCol = len(row)
		}
	}
	return maxRow, maxCol, nil
}
