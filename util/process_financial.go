package util

import (
	"bytes"
	"context"
	"fmt"
	"io"
	stdlog "log"
	"math"
	"net/http"
	"strings"
	"github.com/xuri/excelize/v2"
)


var specialTerms = map[string]float64{
	"rent":         5,
	"insurance":    5,
	"officer":      5,
	"depreciation": 5,
	"professional": 5,
	"accounting":   5,
	"gross profit": 5,
}

func DownloadAndProcess(ctx context.Context, httpClient *http.Client, attachments []Attachment, accounts map[string]AccountsData, supabaseURL, supabaseKey string) (map[string][]byte, map[string][]byte, map[string]ProcessStats, [][]string, error) {
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

	// Download and process financials file.
	results := map[string][]byte{}
	logs := map[string][]byte{}
	statsMap := map[string]ProcessStats{}
	for _, a := range financialsAttachments {
		data, err := downloadAttachment(ctx, httpClient, a)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("download %s: %w", a.FileName, err)
		}
		processed, logBytes, stats, err := ProcessFinancials(ctx, httpClient, data, a.FileName, accounts, tbRows, supabaseURL, supabaseKey)
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
// and for each row whose column A matches a code in accountThresholds:
func ProcessFinancials(ctx context.Context, httpClient *http.Client, data []byte, fileName string, accounts map[string]AccountsData, tbRows [][]string, supabaseURL, supabaseKey string) ([]byte, []byte, ProcessStats, error) {
	financialFile, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("open xlsx: %w", err)
	}
	defer financialFile.Close()

	log, err := NewProcessLogger(fileName)
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("create process logger: %w", err)
	}
	defer log.Close()

	grid, err := initializeGrid(financialFile)
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("initialize grid: %w", err)
	}
	if grid.HeaderRow == -1 || len(grid.MonthCols) == 0 {
		return nil, nil, ProcessStats{}, fmt.Errorf("could not find month column headers in first 10 rows")
	}
	var totalStats ProcessStats
	redStyleCache := map[int]int{}
	yellowStyleCache := map[int]int{}
	greenStyleCache := map[int]int{}
	orangeStyleCache := map[int]int{}

	// MAIN LOOP
	for row := grid.HeaderRow + 1; row <= len(grid.Cells); row++ {
		cells := grid.Cells[row-1]
		if len(cells) == 0 {
			continue
		}

		colA := strings.TrimSpace(cells[0])
		if strings.Contains(strings.ToLower(colA), "total") {
			continue
		}

		account, matched := accounts[strings.ToLower(colA)]
		threshold := account.Threshold

		// Special terms: if column A contains any term from special_terms.xlsx, mark
		// as matched. Only override threshold (and sync to Supabase) when the accounts
		// table has no threshold set for this entry; if it already has one, use it as-is.
		colALower := strings.ToLower(colA)
		for term, termThreshold := range specialTerms {
			if strings.Contains(colALower, term) {
				matched = true
				if threshold == 0 {
					threshold = termThreshold
					if supabaseURL != "" {
						if err := lookupAndUpdateSpecialAccount(ctx, httpClient, supabaseURL, supabaseKey, colALower, termThreshold); err != nil {
							return nil, nil, ProcessStats{}, fmt.Errorf("ensure special account %q: %w", colALower, err)
						}
					}
				}
				break
			}
		}


		if len(accounts) > 0 && !matched {
			// Unmatched rows with a digit code get orange on the last month cell
			// whenever any month cell has a value — regardless of whether the last
			// month cell itself is populated.
			if codePrefix.MatchString(colA) {
				hasAnyValue := false
				for _, col := range grid.MonthCols {
					if col < len(cells) && strings.TrimSpace(cells[col]) != "" {
						hasAnyValue = true
						break
					}
				}
				if hasAnyValue {
					if err := tintLastMonthOrange(financialFile, grid.Sheet, row, grid.MonthCols, orangeStyleCache); err != nil {
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
		for _, col := range grid.MonthCols {
			if col < len(cells) {
				v := strings.TrimSpace(cells[col])
				if v != "" && v != "0" && v != "0.00" {
					allEmpty = false
					break
				}
			}
		}
		if allEmpty {
			for _, col := range grid.MonthCols {
				cellName, err := excelize.CoordinatesToCellName(col+1, row)
				if err != nil {
					continue
				}
				if calc, err := financialFile.CalcCellValue(grid.Sheet, cellName); err == nil && strings.TrimSpace(calc) != "" {
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
		if grid.TotalIncomeCells != nil && (strings.HasPrefix(itemCode, "5") || strings.Contains(colALower, "gross profit")) {
			divisorCells = grid.TotalIncomeCells
		}

		// If matched but no threshold is set, compute one from the row's historical data.
		if threshold == 0 && supabaseURL != "" {
			var normVals []float64
			for _, col := range grid.MonthCols {
				var v float64
				if col < len(cells) && strings.TrimSpace(cells[col]) != "" {
					v, _ = parseAmount(cells[col])
				}
				if divisorCells != nil && col < len(divisorCells) && strings.TrimSpace(divisorCells[col]) != "" {
					d, dErr := parseAmount(divisorCells[col])
					if dErr == nil && d != 0 {
						v /= d
					}
				}
				normVals = append(normVals, v)
			}
			k := account.K
			if k == 0 {
				k = defaultK
			}
			policyMin := account.PolicyMinThreshold
			if policyMin == 0 {
				policyMin = defaultPolicyMinThresh
			}
			computed, stdDev, avgDelta, minVal, maxVal := computeThresholdStats(normVals, k, policyMin)
			if computed > 0 {
				threshold = computed
				if err := UpdateAccountThresholdStats(ctx, httpClient, supabaseURL, supabaseKey, colALower, computed, stdDev, avgDelta, minVal, maxVal, k, policyMin); err != nil {
					stdlog.Printf("warn: update threshold stats for %q: %v", colALower, err)
				}
			}
		}

		// Check if last month cell is empty.
		if len(grid.MonthCols) > 0 {
			lastCol := grid.MonthCols[len(grid.MonthCols)-1]
			if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) == "" {
				log.LogEmptyLastMonth(row, colA)
			}
		}

		var stats ProcessStats

		tinted, err := highlightEmptyCell(financialFile, grid.Sheet, row, cells, grid.MonthCols, redStyleCache)
		if err != nil {
			return nil, nil, ProcessStats{}, fmt.Errorf("highlight empty last month row %d: %w", row, err)
		}
		if tinted {
			stats.Missing++
		}

		if threshold > 0 {
			if len(grid.MonthCols) > 0 {
				lastCol := grid.MonthCols[len(grid.MonthCols)-1]
				if lastCol < len(cells) && strings.TrimSpace(cells[lastCol]) != "" {
					lastVal, err := parseAmount(cells[lastCol])
					if err == nil {
						var rawVals []float64
						var normVals []float64
						var monthHeaders []string
						var sum float64
						for _, col := range grid.MonthCols[:len(grid.MonthCols)-1] {
							var v float64
							if col < len(cells) && strings.TrimSpace(cells[col]) != "" {
								v, _ = parseAmount(cells[col])
							}
							rawVals = append(rawVals, v)
							header := ""
							for _, m := range grid.ParsedMonths {
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

			flagged, err := detectFluctuation(financialFile, grid.Sheet, row, cells, grid.MonthCols, threshold, divisorCells, yellowStyleCache)
			if err != nil {
				return nil, nil, ProcessStats{}, fmt.Errorf("highlight threshold outliers row %d: %w", row, err)
			}
			if flagged {
				stats.Flux++
			} else if !tinted {
				if err := tintGreenLastMonth(financialFile, grid.Sheet, row, cells, grid.MonthCols, greenStyleCache); err != nil {
					return nil, nil, ProcessStats{}, fmt.Errorf("tint green row %d: %w", row, err)
				}
			}
		} else {
			log.LogNoThreshold(row, colA)
			if len(grid.MonthCols) > 0 {
				lastCol := grid.MonthCols[len(grid.MonthCols)-1]
				noteCellName, err := excelize.CoordinatesToCellName(lastCol+3, row)
				if err == nil {
					existing, _ := financialFile.GetCellValue(grid.Sheet, noteCellName)
					existing = strings.TrimSpace(existing)
					note := "NO THRESHOLD FOUND"
					if existing != "" {
						note = existing + " " + note
					}
					financialFile.SetCellValue(grid.Sheet, noteCellName, note)
				}
			}
		}

		totalStats.Missing += stats.Missing
		totalStats.Flux += stats.Flux
	}

	// Reconcile TB Match against the Balance Sheet tab.
	if len(tbRows) > 0 {
		log.LogSection("TB Match Reconciliation")
		inconsistent, err := reconcileTBMatch(financialFile, tbRows, log)
		if err != nil {
			return nil, nil, ProcessStats{}, fmt.Errorf("reconcile tb match: %w", err)
		}
		totalStats.Inconsistent = inconsistent
	}

	if err := insertBznizDaysRow(financialFile, grid); err != nil {
		return nil, nil, ProcessStats{}, err
	}

	var buf bytes.Buffer
	if err := financialFile.Write(&buf); err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("write xlsx: %w", err)
	}
	return buf.Bytes(), log.Bytes(), totalStats, nil
}



