package util

import (
	"bytes"
	"fmt"
	"math"
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
func reconcileTBMatch(f *excelize.File, tbRows [][]string, log *ProcessLogger) error {
	bsSheet, err := findBalanceSheet(f)
	if err != nil {
		return err
	}

	rawRows, err := f.GetRows(bsSheet)
	if err != nil {
		return fmt.Errorf("read balance sheet rows: %w", err)
	}

	maxRow := len(rawRows)
	headerExcelRow, monthCols := findHeaderAndMonthCols(rawRows, maxRow)
	if headerExcelRow == -1 || len(monthCols) == 0 {
		return fmt.Errorf("could not find month headers in balance sheet")
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

	yellowStyleCache := map[int]int{}

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

		// When the name contains a colon (e.g. "21101 Accrued Payroll:Officer Wages Payable"),
		// the segment before the colon is the parent account. Strip it and keep only
		// the digit code + the child name after the colon to match the balance sheet entry.
		category := raw
		if _, child, found := strings.Cut(raw, ":"); found {
			code := strings.SplitN(raw, " ", 2)[0]
			category = code + " " + strings.TrimSpace(child)
		}
		category = strings.ToLower(category)

		debit, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(row[4]), ",", ""), 64)
		credit, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(row[5]), ",", ""), 64)
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
		bsValue, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(cellVal), ",", ""), 64)
		if err != nil {
			continue
		}

		match := tbValue == math.Abs(bsValue)
		log.LogReconcile(category, tbValue, math.Abs(bsValue), match)

		if !match {
			existingID, err := f.GetCellStyle(bsSheet, cellName)
			if err != nil {
				return fmt.Errorf("get cell style %s: %w", cellName, err)
			}
			mergedID, ok := yellowStyleCache[existingID]
			if !ok {
				existing, err := f.GetStyle(existingID)
				if err != nil {
					return fmt.Errorf("get style %s: %w", cellName, err)
				}
				merged, err := f.NewStyle(&excelize.Style{
					Border:    existing.Border,
					Alignment: existing.Alignment,
					Font:      existing.Font,
					Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FFFF00"}, Pattern: 1},
				})
				if err != nil {
					return fmt.Errorf("new yellow style %s: %w", cellName, err)
				}
				yellowStyleCache[existingID] = merged
				mergedID = merged
			}
			if err := f.SetCellStyle(bsSheet, cellName, cellName, mergedID); err != nil {
				return fmt.Errorf("set yellow style %s: %w", cellName, err)
			}
		}
	}
	return nil
}
