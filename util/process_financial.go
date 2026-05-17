package util

import (
	"bytes"
	"context"
	"fmt"
	"io"
	stdlog "log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"github.com/xuri/excelize/v2"
)


const enableOrangeTint = false

type specialTermEntry struct {
	threshold  float64
	termType   string // allowed: revenue, cogs, opex, other
	volatility string // allowed: fixed, variable, semi_variable
}

var specialTerms = map[string]specialTermEntry{
	"rent":         {threshold: 0.05, termType: "opex", volatility: "fixed"},
	"insurance":    {threshold: 0.05, termType: "opex", volatility: "fixed"},
	"depreciation": {threshold: 0.05, termType: "opex", volatility: "fixed"},
	"accounting":   {threshold: 0.05, termType: "opex", volatility: "fixed"},
	"officer":      {threshold: 0.05, termType: "opex", volatility: "semi_variable"},
	"professional": {threshold: 0.05, termType: "opex", volatility: "semi_variable"},
	"gross profit": {threshold: 0.05, termType: "other", volatility: "semi_variable"},
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

	var dollarFloor float64
	if v := os.Getenv("DOLLAR_THRESHOLD"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			dollarFloor = parsed
		}
	}

	grid, err := initializeGrid(financialFile)
	if err != nil {
		return nil, nil, ProcessStats{}, fmt.Errorf("initialize grid: %w", err)
	}
	if grid.HeaderRow == -1 || len(grid.MonthCols) == 0 {
		return nil, nil, ProcessStats{}, fmt.Errorf("could not find month column headers in first 10 rows")
	}
	var closingMonth string
	if len(grid.ParsedMonths) > 0 {
		last := grid.ParsedMonths[len(grid.ParsedMonths)-1]
		closingMonth = fmt.Sprintf("%02d-%d", int(last.month), last.year)
	}

	// Write analysis column headers after the last month column.
	analysisLastCol := grid.MonthCols[len(grid.MonthCols)-1]
	for i, h := range []string{"threshold", "confidence", "flag_review", "agent_k_threshold", "justification"} {
		if cellName, err := excelize.CoordinatesToCellName(analysisLastCol+3+i, grid.HeaderRow); err == nil {
			financialFile.SetCellValue(grid.Sheet, cellName, h)
		}
	}

	var totalStats ProcessStats
	redStyleCache := map[int]int{}
	yellowStyleCache := map[int]int{}
	greenStyleCache := map[int]int{}
	orangeStyleCache := map[int]int{}
	blueStyleCache := map[int]int{}

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
		if isBalanceSheetCode(colA) {
			continue
		}

		account, matched := accounts[strings.ToLower(colA)]

		var threshold float64
		var hasStoredThreshold bool
		for _, entry := range account.ThresholdEntries {
			if strings.EqualFold(entry.Company.Name, grid.CompanyName) {
				for _, tv := range entry.Thresholds {
					if tv.ClosingMonth == closingMonth {
						threshold = tv.Value
						hasStoredThreshold = true
						break
					}
				}
				break
			}
		}

		// Special terms: mark as matched and ensure the account row exists in Supabase.
		// Use the special term threshold only as a fallback if no computed threshold is stored.
		colALower := strings.ToLower(colA)
		var specialTermThreshold float64
		for term, entry := range specialTerms {
			if strings.Contains(colALower, term) {
				matched = true
				specialTermThreshold = entry.threshold
				if supabaseURL != "" {
					if err := lookupAndUpdateSpecialAccount(ctx, httpClient, supabaseURL, supabaseKey, colALower, entry.threshold, entry.termType, entry.volatility); err != nil {
						return nil, nil, ProcessStats{}, fmt.Errorf("lookup and update special account %q: %w", colALower, err)
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
				if hasAnyValue && enableOrangeTint {
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

		// If the last month cell is empty, tint red and skip all processing for this row.
		if len(grid.MonthCols) > 0 {
			lastCol := grid.MonthCols[len(grid.MonthCols)-1]
			if lastCol >= len(cells) || strings.TrimSpace(cells[lastCol]) == "" {
				log.LogEmptyLastMonth(row, colA)
				if _, err := highlightEmptyCell(financialFile, grid.Sheet, row, cells, grid.MonthCols, redStyleCache); err != nil {
					return nil, nil, ProcessStats{}, fmt.Errorf("highlight empty last month row %d: %w", row, err)
				}
				totalStats.Missing++
				continue
			}
		}

		// Collect raw monthly values for history tracking (all months, raw amounts).
		historyVals := make(map[string]float64)
		for _, col := range grid.MonthCols {
			if col >= len(cells) || strings.TrimSpace(cells[col]) == "" {
				continue
			}
			v, parseErr := parseAmount(cells[col])
			if parseErr != nil {
				continue
			}
			for _, m := range grid.ParsedMonths {
				if m.col == col+1 {
					historyVals[fmt.Sprintf("%02d-%d", int(m.month), m.year)] = v
					break
				}
			}
		}

		// Determine if this row should normalize against total income.
		// Code-5 rows and rows matching 'gross profit' both divide by total income.
		itemCode := strings.SplitN(strings.TrimSpace(colA), " ", 2)[0]
		var divisorCells []string
		if grid.TotalIncomeCells != nil && (strings.HasPrefix(itemCode, "5") || strings.Contains(colALower, "gross profit")) {
			divisorCells = grid.TotalIncomeCells
		}

		patchCode := account.Code
		if patchCode == "" {
			patchCode = colALower
		}

		k, kFound := 0.0, false
		if kVal, ok := findKForCompany(account.KEntries, grid.CompanyName); ok {
			k = kVal
			kFound = true
		}

		if !hasStoredThreshold && !kFound {
			log.LogNoK(row, colA)
			if err := tintBlueLastMonth(financialFile, grid.Sheet, row, cells, grid.MonthCols, blueStyleCache); err != nil {
				return nil, nil, ProcessStats{}, fmt.Errorf("tint blue row %d: %w", row, err)
			}
			continue
		}

		policyMin := account.PolicyMinThreshold
		if policyMin == 0 {
			policyMin = defaultPolicyMinThresh
		}

		// If no stored threshold exists for this company+month, compute one from the row's data.
		if !hasStoredThreshold && supabaseURL != "" {
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
			computed, _, _ := computeThresholdStats(normVals, k, policyMin)
			if computed > 0 {
				threshold = computed
			} else {
				threshold = specialTermThreshold
			}
			if computed > 0 {
				updatedEntries := appendThresholdValue(account.ThresholdEntries, grid.CompanyName, closingMonth, computed, 0)
				if err := PatchAccountThreshold(ctx, httpClient, supabaseURL, supabaseKey, patchCode, updatedEntries); err != nil {
					stdlog.Printf("warn: patch threshold for %q: %v", colALower, err)
				}
			}
		}

		// Upsert per-company history and derive avg_absdelta + flag_rate from full history.
		var histFlagRate, histAvgAbsDelta float64
		updatedHistEntries := account.HistoryEntries
		for monthStr, val := range historyVals {
			updatedHistEntries = upsertHistoryEntry(updatedHistEntries, grid.CompanyName, monthStr, val)
		}
		histAvgAbsDelta, histFlagRate = computeHistoryMetrics(updatedHistEntries, grid.CompanyName, threshold)
		updatedHistEntries = updateHistoryAvgAbsDelta(updatedHistEntries, grid.CompanyName, histAvgAbsDelta)
		if supabaseURL != "" {
			if err := PatchAccountHistoryAndAvgAbsDelta(ctx, httpClient, supabaseURL, supabaseKey, patchCode, updatedHistEntries); err != nil {
				stdlog.Printf("warn: patch history for %q: %v", colALower, err)
			}
		}

		// Write threshold value into the analysis columns for this row.
		if threshold > 0 {
			if cellName, err := excelize.CoordinatesToCellName(analysisLastCol+3, row); err == nil {
				financialFile.SetCellValue(grid.Sheet, cellName, threshold)
			}
		}

		var stats ProcessStats
		var dollarDelta float64
		var fluctuationStatus string

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
							dollarDelta = math.Abs(lastVal - avg)
							effectiveLast := lastVal
							if divisorCells != nil && lastCol < len(divisorCells) && strings.TrimSpace(divisorCells[lastCol]) != "" {
								d, dErr := parseAmount(divisorCells[lastCol])
								if dErr == nil && d != 0 {
									effectiveLast = lastVal / d
								}
							}
							pctDiff := math.Abs(effectiveLast-avg) / math.Abs(avg)
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
			if flagged && dollarFloor > 0 && dollarDelta < dollarFloor {
				fluctuationStatus = "stable"
				// Percent threshold breached but dollar movement too small — tint green.
				if err := tintGreenLastMonth(financialFile, grid.Sheet, row, cells, grid.MonthCols, greenStyleCache); err != nil {
					return nil, nil, ProcessStats{}, fmt.Errorf("tint green (dollar floor) row %d: %w", row, err)
				}
			} else if flagged {
				fluctuationStatus = "fluctuating"
				stats.Flux++
			} else {
				fluctuationStatus = "stable"
				if err := tintGreenLastMonth(financialFile, grid.Sheet, row, cells, grid.MonthCols, greenStyleCache); err != nil {
					return nil, nil, ProcessStats{}, fmt.Errorf("tint green row %d: %w", row, err)
				}
			}
			if supabaseURL != "" {
				updatedKEntries := upsertKFlagRate(account.KEntries, grid.CompanyName, k, histFlagRate)
				if err := PatchAccountKAndFlagRate(ctx, httpClient, supabaseURL, supabaseKey, patchCode, updatedKEntries); err != nil {
					stdlog.Printf("warn: patch k_and_flagrate for %q: %v", colALower, err)
				}
			}
			if fluctuationStatus == "fluctuating" || fluctuationStatus == "stable" {
				var agentHistory []map[string]float64
				for _, e := range updatedHistEntries {
					if strings.EqualFold(e.Company.Name, grid.CompanyName) {
						agentHistory = e.History
						break
					}
				}
				agentResult, agentErr := callClaudeAgent(ctx, httpClient, agentPayload{
					AccountCode:       patchCode,
					AccountType:       account.Type,
					Volatility:        account.Volatility,
					ThresholdUsed:     threshold,
					KUsed:             k,
					FlagRate:          histFlagRate,
					FluctuationStatus: fluctuationStatus,
					AvgAbsDelta:       histAvgAbsDelta,
					History:           agentHistory,
				})
				if agentErr != nil {
					stdlog.Printf("warn: claude agent for %q: %v", colALower, agentErr)
				} else {
					if cellName, err := excelize.CoordinatesToCellName(analysisLastCol+6, row); err == nil {
						financialFile.SetCellValue(grid.Sheet, cellName, agentResult.AgentKThreshold)
					}
					if cellName, err := excelize.CoordinatesToCellName(analysisLastCol+7, row); err == nil {
						financialFile.SetCellValue(grid.Sheet, cellName, agentResult.Justification)
					}
				}
			}
		} else {
			log.LogNoThreshold(row, colA)
		}

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



