package util

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// ProcessStats holds counts of flagged cells produced during processing.
type ProcessStats struct {
	Missing      int // red-tinted last month cells (empty)
	Flux         int // yellow-tinted last month cells (fluctuation)
	Inconsistent int // yellow-tinted balance sheet cells (TB Match mismatch)
}

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

// LoadCategoryNamesTestMode loads categories_index.xlsx and categories_index_removed.xlsx,
// assigning each entry a random threshold between 0 and 25 (neither file has thresholds populated).
func LoadCategoryNamesTestMode() (map[string]float64, error) {
	categories := map[string]float64{}
	for _, path := range []string{"categories_index.xlsx", "categories_index_removed.xlsx"} {
		f, err := excelize.OpenFile(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		for _, sheet := range f.GetSheetList() {
			rows, err := f.GetRows(sheet)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("get rows %s/%s: %w", path, sheet, err)
			}
			for i, row := range rows {
				if i == 0 {
					continue
				}
				if len(row) == 0 || strings.TrimSpace(row[0]) == "" {
					continue
				}
				name := strings.ToLower(strings.TrimSpace(row[0]))
				categories[name] = math.Round(rand.Float64()*25*10) / 10
			}
		}
		f.Close()
	}
	return categories, nil
}

// LoadSpecialTermsFromXLSX reads special_terms.xlsx and returns a map of
// lowercase term → threshold. Rows whose column A contains a matching term
// (substring) take priority over categories_index matches during processing.
// When randomThresholds is true, each entry is assigned a random value 0–25
// instead of reading col B (used in TEST mode).
func LoadSpecialTermsFromXLSX(xlsxPath string, randomThresholds bool) (map[string]float64, error) {
	f, err := excelize.OpenFile(xlsxPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", xlsxPath, err)
	}
	defer f.Close()

	terms := map[string]float64{}
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return nil, fmt.Errorf("get rows %s/%s: %w", xlsxPath, sheet, err)
		}
		for i, row := range rows {
			if i == 0 {
				continue // skip header
			}
			if len(row) == 0 || strings.TrimSpace(row[0]) == "" {
				continue
			}
			term := strings.ToLower(strings.TrimSpace(row[0]))
			var threshold float64
			if randomThresholds {
				threshold = math.Round(rand.Float64()*25*10) / 10
			} else if len(row) >= 2 && strings.TrimSpace(row[1]) != "" {
				if v, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64); err == nil {
					threshold = v
				}
			}
			terms[term] = threshold
		}
	}
	return terms, nil
}

// DownloadAndProcess downloads xlsx attachments for the task.
// If two files are attached, the file whose name contains "financial" is
// processed with ProcessFinancials; the other is loaded via LoadTBMatch.
// The second return value contains the TB Match rows (nil when only one file).
func DownloadAndProcess(ctx context.Context, httpClient *http.Client, attachments []Attachment, categoryNames map[string]float64, specialTerms map[string]float64) (map[string][]byte, map[string][]byte, map[string]ProcessStats, [][]string, error) {
	// Separate financials file from the TB workbook when two files are present.
	var financialsAttachments []Attachment
	var tbAttachment *Attachment

	for i, a := range attachments {
		if !strings.HasSuffix(strings.ToLower(a.FileName), ".xlsx") {
			continue
		}
		if len(attachments) == 2 && !strings.Contains(strings.ToLower(a.FileName), "financial") {
			tbAttachment = &attachments[i]
		} else {
			financialsAttachments = append(financialsAttachments, a)
		}
	}

	// Download and parse the TB Match workbook if present.
	var tbRows [][]string
	if tbAttachment != nil {
		data, err := downloadAttachment(ctx, httpClient, *tbAttachment)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("download %s: %w", tbAttachment.FileName, err)
		}
		tbRows, err = LoadTBMatch(data)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("load tb match %s: %w", tbAttachment.FileName, err)
		}
	}

	// Download and process each financials file.
	results := map[string][]byte{}
	logs := map[string][]byte{}
	statsMap := map[string]ProcessStats{}
	for _, a := range financialsAttachments {
		data, err := downloadAttachment(ctx, httpClient, a)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("download %s: %w", a.FileName, err)
		}
		processed, logBytes, stats, err := ProcessFinancials(data, a.FileName, categoryNames, specialTerms, tbRows)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("process %s: %w", a.FileName, err)
		}
		results[a.FileName] = processed
		logs[a.FileName] = logBytes
		statsMap[a.FileName] = stats
	}
	return results, logs, statsMap, tbRows, nil
}

func downloadAttachment(ctx context.Context, httpClient *http.Client, a Attachment) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.DownloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return data, nil
}

// ProcessFinancials opens an xlsx file, finds the Income Statement / Profit & Loss sheet,
// and for each row whose column A matches a category in categoryNames:
//   - highlights empty month cells yellow (partial missing data)
//   - highlights the last month cell red if it falls outside avg±threshold
//
// Rows not matching any category are skipped entirely.
func ProcessFinancials(data []byte, fileName string, categoryNames map[string]float64, specialTerms map[string]float64, tbRows [][]string) ([]byte, []byte, ProcessStats, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("open xlsx: %w", err)
	}
	defer f.Close()

	log, err := NewProcessLogger(fileName)
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("create process logger: %w", err)
	}
	defer log.Close()

	sheetName, err := findIncomeSheet(f)
	if err != nil {
		return nil, nil, ProcessStats{}, err
	}

	maxRow, maxCol, err := sheetDimensions(f, sheetName)
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("get sheet dimensions: %w", err)
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
		return nil, nil, ProcessStats{}, fmt.Errorf("get rows for header scan: %w", err)
	}
	headerExcelRow, monthCols := findHeaderAndMonthCols(rawRows, maxRow)
	if headerExcelRow == -1 || len(monthCols) == 0 {
		return nil, nil, ProcessStats{}, fmt.Errorf("could not find month column headers in first 10 rows")
	}

	// Parse month/year from each header cell for the business days row inserted later.
	type monthInfo struct {
		col   int // 1-based Excel column
		year  int
		month time.Month
	}
	var parsedMonths []monthInfo
	headerRow := rawRows[headerExcelRow-1]
	for _, col := range monthCols {
		if col >= len(headerRow) {
			continue
		}
		t, err := time.Parse("Jan 2006", strings.TrimSpace(headerRow[col]))
		if err != nil {
			continue
		}
		parsedMonths = append(parsedMonths, monthInfo{col: col + 1, year: t.Year(), month: t.Month()})
	}

	// Pre-scan for the "Total Income" row to use as divisor for code-5 rows.
	// This row always appears above any code-5 item rows in the sheet.
	// Total Income cells may contain cell-reference formulas (e.g. "(B7)+(B8)"), so
	// we evaluate them with CalcCellValue rather than relying on the grid values.
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
			evaluated := make([]string, len(c))
			for i, v := range c {
				evaluated[i] = v
				if v == "" {
					cellName, err := excelize.CoordinatesToCellName(i+1, row)
					if err != nil {
						continue
					}
					if calc, err := f.CalcCellValue(sheetName, cellName); err == nil && calc != "" {
						evaluated[i] = calc
					}
				}
			}
			totalCells = evaluated
			break
		}
	}

	var totalStats ProcessStats
	redStyleCache := map[int]int{}
	yellowStyleCache := map[int]int{}
	greenStyleCache := map[int]int{}
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

		// Filter by categories_index.xlsx: match full column A value.
		threshold, matched := categoryNames[strings.ToLower(colA)]

		// Special terms override: if column A contains any term from special_terms.xlsx,
		// use that threshold instead (substring match, takes priority over categories_index).
		colALower := strings.ToLower(colA)
		for term, termThreshold := range specialTerms {
			if strings.Contains(colALower, term) {
				threshold = termThreshold
				matched = true
				break
			}
		}

		if len(categoryNames) > 0 && !matched {
			// Unmatched rows with a digit code get orange on the last month cell
			// whenever any month cell has a value — regardless of whether the last
			// month cell itself is populated.
			if codePrefix.MatchString(colA) {
				hasAnyValue := false
				for _, col := range monthCols {
					if col < len(cells) && strings.TrimSpace(cells[col]) != "" {
						hasAnyValue = true
						break
					}
				}
				if hasAnyValue {
					if err := tintLastMonthOrange(f, sheetName, row, monthCols, orangeStyleCache); err != nil {
						return nil, nil, ProcessStats{}, fmt.Errorf("tint orange row %d: %w", row, err)
					}
				}
			}
			continue
		}

		log.LogMatch(row, colA)

		// Skip rows where every month cell is empty — nothing to evaluate.
		// If all cells are empty, retry with CalcCellValue in case they contain
		// cell-reference formulas that weren't resolved during grid building.
		allEmpty := true
		for _, col := range monthCols {
			if col < len(cells) && strings.TrimSpace(cells[col]) != "" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			for _, col := range monthCols {
				cellName, err := excelize.CoordinatesToCellName(col+1, row)
				if err != nil {
					continue
				}
				if calc, err := f.CalcCellValue(sheetName, cellName); err == nil && strings.TrimSpace(calc) != "" {
					if col < len(cells) {
						cells[col] = calc
					}
					allEmpty = false
				}
			}
		}
		if allEmpty {
			log.LogAllEmpty(row, colA)
			continue
		}

		// Determine if this row should normalize against total income.
		// Code-5 rows and rows matching 'gross profit' both divide by total income.
		itemCode := strings.SplitN(strings.TrimSpace(colA), " ", 2)[0]
		var divisorCells []string
		if totalCells != nil && (strings.HasPrefix(itemCode, "5") || strings.Contains(colALower, "gross profit")) {
			divisorCells = totalCells
		}

		// Check if last month cell is empty.
		if len(monthCols) > 0 {
			lastCol := monthCols[len(monthCols)-1]
			if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) == "" {
				log.LogEmptyLastMonth(row, colA)
			}
		}

		var stats ProcessStats

		tinted, err := highlightEmptyCell(f, sheetName, row, cells, monthCols, redStyleCache)
		if err != nil {
			return nil, nil, ProcessStats{}, fmt.Errorf("highlight empty last month row %d: %w", row, err)
		}
		if tinted {
			stats.Missing++
		}

		if threshold > 0 {
			if len(monthCols) > 0 {
				lastCol := monthCols[len(monthCols)-1]
				if lastCol < len(cells) && strings.TrimSpace(cells[lastCol]) != "" {
					lastVal, err := parseAmount(cells[lastCol])
					if err == nil {
						var rawVals []float64
						var normVals []float64
						var monthHeaders []string
						var sum float64
						for _, col := range monthCols[:len(monthCols)-1] {
							var v float64
							if col < len(cells) && strings.TrimSpace(cells[col]) != "" {
								v, _ = parseAmount(cells[col])
							}
							rawVals = append(rawVals, v)
							header := ""
							for _, m := range parsedMonths {
								if m.col == col+1 {
									header = m.month.String()[:3] + " " + fmt.Sprintf("%d", m.year)
									break
								}
							}
							monthHeaders = append(monthHeaders, header)
							nv := v
							if divisorCells != nil && col < len(divisorCells) && strings.TrimSpace(divisorCells[col]) != "" {
								d, dErr := parseAmount(divisorCells[col])
								if dErr == nil && d != 0 {
									nv = v / d
								}
							}
							normVals = append(normVals, nv)
							sum += nv
						}
						if len(rawVals) > 0 {
							avg := sum / float64(len(rawVals))
							effectiveLast := lastVal
							if divisorCells != nil && lastCol < len(divisorCells) && strings.TrimSpace(divisorCells[lastCol]) != "" {
								d, dErr := parseAmount(divisorCells[lastCol])
								if dErr == nil && d != 0 {
									effectiveLast = lastVal / d
								}
							}
							pctDiff := math.Abs(effectiveLast-avg) / math.Abs(avg) * 100
							flagged := pctDiff > threshold
							log.LogFluctuation(row, colA, monthHeaders, rawVals, normVals, divisorCells != nil, avg, lastVal, effectiveLast, pctDiff, threshold, flagged)
						}
					}
				}
			} else {
				log.LogNoThreshold(row, colA)
			}

			flagged, err := detectFluctuation(f, sheetName, row, cells, monthCols, threshold, divisorCells, yellowStyleCache)
			if err != nil {
				return nil, nil, ProcessStats{}, fmt.Errorf("highlight threshold outliers row %d: %w", row, err)
			}
			if flagged {
				stats.Flux++
			} else if !tinted {
				if err := tintGreenLastMonth(f, sheetName, row, cells, monthCols, greenStyleCache); err != nil {
					return nil, nil, ProcessStats{}, fmt.Errorf("tint green row %d: %w", row, err)
				}
			}
		} else {
			log.LogNoThreshold(row, colA)
			if !tinted {
				if err := tintGreenLastMonth(f, sheetName, row, cells, monthCols, greenStyleCache); err != nil {
					return nil, nil, ProcessStats{}, fmt.Errorf("tint green row %d: %w", row, err)
				}
			}
		}

		totalStats.Missing += stats.Missing
		totalStats.Flux += stats.Flux
	}

	// Reconcile TB Match against the Balance Sheet tab.
	if len(tbRows) > 0 {
		log.LogSection("TB Match Reconciliation")
		inconsistent, err := reconcileTBMatch(f, tbRows, log)
		if err != nil {
			return nil, nil, ProcessStats{}, fmt.Errorf("reconcile tb match: %w", err)
		}
		totalStats.Inconsistent = inconsistent
	}

	// Check if a Business Days row already exists directly above the month header row.
	// If so, skip insertion entirely to avoid duplicating on reprocessed files.
	alreadyHasBusinessDays := false
	if headerExcelRow > 1 {
		aboveCell, _ := excelize.CoordinatesToCellName(1, headerExcelRow-1)
		aboveVal, _ := f.GetCellValue(sheetName, aboveCell)
		if strings.EqualFold(strings.TrimSpace(aboveVal), "business days") {
			alreadyHasBusinessDays = true
		}
	}

	// Insert the business days row directly above the month header row.
	if !alreadyHasBusinessDays {
	if err := f.InsertRows(sheetName, headerExcelRow, 1); err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("insert business days row: %w", err)
	}
	boldStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("create bold style: %w", err)
	}
	intStyle, err := f.NewStyle(&excelize.Style{NumFmt: 1}) // 1 = "0" (plain integer)
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("create integer style: %w", err)
	}
	labelCell, _ := excelize.CoordinatesToCellName(1, headerExcelRow)
	if err := f.SetCellValue(sheetName, labelCell, "Business Days"); err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("set business days label: %w", err)
	}
	if err := f.SetCellStyle(sheetName, labelCell, labelCell, boldStyle); err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("set bold style on business days label: %w", err)
	}
	for _, m := range parsedMonths {
		cellName, _ := excelize.CoordinatesToCellName(m.col, headerExcelRow)
		if err := f.SetCellValue(sheetName, cellName, workdaysInMonth(m.year, m.month)); err != nil {
			return nil, nil, ProcessStats{}, fmt.Errorf("set workdays %s: %w", cellName, err)
		}
		if err := f.SetCellStyle(sheetName, cellName, cellName, intStyle); err != nil {
			return nil, nil, ProcessStats{}, fmt.Errorf("set integer style %s: %w", cellName, err)
		}
	}
	} // end !alreadyHasBusinessDays

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("write xlsx: %w", err)
	}
	return buf.Bytes(), log.Bytes(), totalStats, nil
}

// workdaysInMonth returns the number of Mon–Fri days in the given month and year.
func workdaysInMonth(year int, month time.Month) int {
	count := 0
	d := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	for d.Month() == month {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			count++
		}
		d = d.AddDate(0, 0, 1)
	}
	return count
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
