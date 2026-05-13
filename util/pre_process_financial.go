package util

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

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

// findIncomeSheet returns the name of the first sheet containing "income", "profit", or "loss".
func findPnlSheet(f *excelize.File) (string, error) {
	for _, name := range f.GetSheetList() {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "income") || strings.Contains(lower, "profit") || strings.Contains(lower, "loss") {
			return name, nil
		}
	}
	return "", fmt.Errorf("no income statement / profit & loss sheet found")
}

// monthInfo holds the parsed month/year and 1-based column index for a single month header cell.
type monthInfo struct {
	col   int
	year  int
	month time.Month
}

// SheetGrid holds the 2-D cell data for a sheet together with the structural
// metadata discovered during initialization.
type SheetGrid struct {
	Sheet            string      // name of the Excel sheet this grid was built from
	CompanyName      string      // value of cell A1 — the client's company name
	Cells            [][]string  // [row-1][col-1], built from GetCellValue with formula fallback
	HeaderRow        int         // 1-based Excel row containing month headers; -1 if not found
	MonthCols        []int       // 0-based column indices of month columns
	TotalIncomeCells []string    // evaluated cells of the "Total Income" row; nil if not found
	ParsedMonths     []monthInfo // parsed month/year for each month column header
}

// extractCompanyName returns the trimmed value of cell A1, which by convention
// contains the client's company name in the P&L sheet.
func extractCompanyName(cells [][]string) string {
	if len(cells) == 0 || len(cells[0]) == 0 {
		return ""
	}
	return strings.TrimSpace(cells[0][0])
}



// initializeGrid finds the P&L sheet and builds a SheetGrid from it.
func initializeGrid(f *excelize.File) (*SheetGrid, error) {
	sheet, err := findPnlSheet(f)
	if err != nil {
		return nil, err
	}
	return buildSheetGrid(f, sheet)
}

func buildSheetGrid(f *excelize.File, sheet string) (*SheetGrid, error) {
	maxRow, maxCol, err := sheetDimensions(f, sheet)
	if err != nil {
		return nil, fmt.Errorf("sheet dimensions: %w", err)
	}

	rawRows, err := f.GetRows(sheet)
	if err != nil {
		return nil, fmt.Errorf("get rows for header scan: %w", err)
	}

	headerRow := -1
	var monthCols []int
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
			headerRow = row
			monthCols = cols
			break
		}
	}

	var parsedMonths []monthInfo
	if headerRow != -1 {
		headerCells := rawRows[headerRow-1]
		for _, col := range monthCols {
			if col >= len(headerCells) {
				continue
			}
			t, err := time.Parse("Jan 2006", strings.TrimSpace(headerCells[col]))
			if err != nil {
				continue
			}
			parsedMonths = append(parsedMonths, monthInfo{col: col + 1, year: t.Year(), month: t.Month()})
		}
	}

	cells := make([][]string, maxRow)
	for row := 1; row <= maxRow; row++ {
		cells[row-1] = make([]string, maxCol)
		for col := 1; col <= maxCol; col++ {
			cellName, _ := excelize.CoordinatesToCellName(col, row)
			val, _ := f.GetCellValue(sheet, cellName)
			if val == "" {
				if formula, _ := f.GetCellFormula(sheet, cellName); formula != "" {
					if _, err := strconv.ParseFloat(strings.ReplaceAll(formula, ",", ""), 64); err == nil {
						val = formula
					}
				}
			}
			cells[row-1][col-1] = val
		}
	}

	// Scan for the "Total Income" row so callers can normalise code-5 amounts.
	var totalCells []string
	if headerRow != -1 {
		for row := headerRow + 1; row <= maxRow; row++ {
			if row-1 >= len(cells) {
				break
			}
			c := cells[row-1]
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
						if calc, err := f.CalcCellValue(sheet, cellName); err == nil && calc != "" {
							evaluated[i] = calc
						}
					}
				}
				totalCells = evaluated
				break
			}
		}
	}

	return &SheetGrid{Sheet: sheet, CompanyName: extractCompanyName(cells), Cells: cells, HeaderRow: headerRow, MonthCols: monthCols, TotalIncomeCells: totalCells, ParsedMonths: parsedMonths}, nil
}

func sheetDimensions(f *excelize.File, sheet string) (maxRow, maxCol int, err error) {
	dim, _ := f.GetSheetDimension(sheet)
	// if GetSheetDimension succeeds, retrieve maxrow and maxcol
	if dim != "" {
		_, maxRow, maxCol, err = parseDimension(dim)
		if err == nil {
			return
		}
	}

	// else, extract the LONG way
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

// bznizDaysInMonth returns the number of Mon–Fri days in the given month and year.
func bznizDaysInMonth(year int, month time.Month) int {
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

// insertBznizDaysRow inserts a "Business Days" row directly above the month header row.
// If the row already exists (reprocessed file), the insertion is skipped.
func insertBznizDaysRow(f *excelize.File, grid *SheetGrid) error {
	if grid.HeaderRow > 1 {
		aboveCell, _ := excelize.CoordinatesToCellName(1, grid.HeaderRow-1)
		aboveVal, _ := f.GetCellValue(grid.Sheet, aboveCell)
		if strings.EqualFold(strings.TrimSpace(aboveVal), "business days") {
			return nil
		}
	}
	if err := f.InsertRows(grid.Sheet, grid.HeaderRow, 1); err != nil {
		return fmt.Errorf("insert business days row: %w", err)
	}
	boldStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return fmt.Errorf("create bold style: %w", err)
	}
	intStyle, err := f.NewStyle(&excelize.Style{NumFmt: 1})
	if err != nil {
		return fmt.Errorf("create integer style: %w", err)
	}
	labelCell, _ := excelize.CoordinatesToCellName(1, grid.HeaderRow)
	if err := f.SetCellValue(grid.Sheet, labelCell, "Business Days"); err != nil {
		return fmt.Errorf("set business days label: %w", err)
	}
	if err := f.SetCellStyle(grid.Sheet, labelCell, labelCell, boldStyle); err != nil {
		return fmt.Errorf("set bold style on business days label: %w", err)
	}
	for _, m := range grid.ParsedMonths {
		cellName, _ := excelize.CoordinatesToCellName(m.col, grid.HeaderRow)
		if err := f.SetCellValue(grid.Sheet, cellName, bznizDaysInMonth(m.year, m.month)); err != nil {
			return fmt.Errorf("set business days %s: %w", cellName, err)
		}
		if err := f.SetCellStyle(grid.Sheet, cellName, cellName, intStyle); err != nil {
			return fmt.Errorf("set integer style %s: %w", cellName, err)
		}
	}
	return nil
}

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

