package util

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

const tbMatchSheet = "TB Match"

// LoadTBMatch opens an xlsx file from raw bytes and returns all rows from the
// "TB Match" tab. No other tab is read.
func LoadTBMatch(data []byte) ([][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open workbook: %w", err)
	}
	defer f.Close()

	found := false
	for _, name := range f.GetSheetList() {
		if strings.EqualFold(name, tbMatchSheet) {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("sheet %q not found in workbook", tbMatchSheet)
	}

	rows, err := f.GetRows(tbMatchSheet)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", tbMatchSheet, err)
	}
	return rows, nil
}

// findBalanceSheet returns the name of the first sheet containing "balance sheet".
func findBalanceSheet(f *excelize.File) (string, error) {
	for _, name := range f.GetSheetList() {
		if strings.Contains(strings.ToLower(name), "balance") {
			return name, nil
		}
	}
	return "", fmt.Errorf("no balance sheet tab found")
}

// reconcileTBMatch compares each TB Match row's |debit - credit| (columns E and F)
// against the absolute value of the last month cell for the matching category in
// the Balance Sheet tab. When they differ, an asterisk is appended to the cell value.
func reconcileTBMatch(f *excelize.File, tbRows [][]string, log *ProcessLogger) (int, error) {
	bsSheet, err := findBalanceSheet(f)
	if err != nil {
		return 0, err
	}

	rawRows, err := f.GetRows(bsSheet)
	if err != nil {
		return 0, fmt.Errorf("read balance sheet rows: %w", err)
	}

	maxRow := len(rawRows)
	headerExcelRow, monthCols := findHeaderAndMonthCols(rawRows, maxRow)
	if headerExcelRow == -1 || len(monthCols) == 0 {
		return 0, fmt.Errorf("could not find month headers in balance sheet")
	}
	lastCol := monthCols[len(monthCols)-1]

	// Build a map of lowercase category name → 1-based Excel row number.
	categoryRow := map[string]int{}
	for i := headerExcelRow; i < len(rawRows); i++ {
		if len(rawRows[i]) == 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(rawRows[i][0]))
		if name != "" {
			categoryRow[name] = i + 1 // convert to 1-based
		}
	}

	reconThreshold := 0.0
	if v, err := strconv.ParseFloat(os.Getenv("RECON_THRESHOLD"), 64); err == nil && v >= 0 {
		reconThreshold = v
	}

	inconsistent := 0
	yellowStyleCache := map[int]int{}
	greenStyleCache := map[int]int{}

	for i, row := range tbRows {
		if i <= 1 {
			continue // skip date row and header row
		}
		if len(row) < 6 {
			continue
		}

		// Only process rows whose category starts with a digit code.
		raw := strings.TrimSpace(row[0])
		if raw == "" || !strings.ContainsAny(string(raw[0]), "0123456789") {
			continue
		}

		// When the name contains colons (e.g. "12135 Prepaid Expenses:Prepaid Insurance:Health Insurance"),
		// take the leading digit code and the last colon segment to match the balance sheet entry.
		category := raw
		if idx := strings.LastIndex(raw, ":"); idx >= 0 {
			code := strings.SplitN(raw, " ", 2)[0]
			category = code + " " + strings.TrimSpace(raw[idx+1:])
		}
		category = strings.ToLower(category)

		debit, _ := parseAmount(row[4])
		credit, _ := parseAmount(row[5])
		tbValue := math.Abs(debit - credit)

		rowNum, ok := categoryRow[category]
		if !ok {
			continue
		}

		cellName, err := excelize.CoordinatesToCellName(lastCol+1, rowNum)
		if err != nil {
			continue
		}
		cellVal, err := f.GetCellValue(bsSheet, cellName)
		if err != nil {
			continue
		}
		if strings.TrimSpace(cellVal) == "" {
			if formula, _ := f.GetCellFormula(bsSheet, cellName); formula != "" {
				if _, ferr := strconv.ParseFloat(strings.ReplaceAll(formula, ",", ""), 64); ferr == nil {
					cellVal = formula
				} else if calc, cerr := f.CalcCellValue(bsSheet, cellName); cerr == nil && strings.TrimSpace(calc) != "" {
					cellVal = calc
				}
			}
		}
		if strings.TrimSpace(cellVal) == "" {
			continue
		}
		bsValue, err := parseAmount(cellVal)
		if err != nil {
			continue
		}

		match := math.Abs(tbValue-math.Abs(bsValue)) <= reconThreshold
		log.LogReconcile(category, tbValue, math.Abs(bsValue), match)

		existingID, err := f.GetCellStyle(bsSheet, cellName)
		if err != nil {
			return 0, fmt.Errorf("get cell style %s: %w", cellName, err)
		}

		if match {
			mergedID, ok := greenStyleCache[existingID]
			if !ok {
				existing, err := f.GetStyle(existingID)
				if err != nil {
					return 0, fmt.Errorf("get style %s: %w", cellName, err)
				}
				merged, err := f.NewStyle(&excelize.Style{
					Border:    existing.Border,
					Alignment: existing.Alignment,
					Font:      existing.Font,
					Fill:      excelize.Fill{Type: "pattern", Color: []string{"#00B050"}, Pattern: 1},
				})
				if err != nil {
					return 0, fmt.Errorf("new green style %s: %w", cellName, err)
				}
				greenStyleCache[existingID] = merged
				mergedID = merged
			}
			if err := f.SetCellStyle(bsSheet, cellName, cellName, mergedID); err != nil {
				return 0, fmt.Errorf("set green style %s: %w", cellName, err)
			}
		} else {
			inconsistent++
			mergedID, ok := yellowStyleCache[existingID]
			if !ok {
				existing, err := f.GetStyle(existingID)
				if err != nil {
					return 0, fmt.Errorf("get style %s: %w", cellName, err)
				}
				merged, err := f.NewStyle(&excelize.Style{
					Border:    existing.Border,
					Alignment: existing.Alignment,
					Font:      existing.Font,
					Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FFFF00"}, Pattern: 1},
				})
				if err != nil {
					return 0, fmt.Errorf("new yellow style %s: %w", cellName, err)
				}
				yellowStyleCache[existingID] = merged
				mergedID = merged
			}
			if err := f.SetCellStyle(bsSheet, cellName, cellName, mergedID); err != nil {
				return 0, fmt.Errorf("set yellow style %s: %w", cellName, err)
			}
		}
	}
	return inconsistent, nil
}
