package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mutouyun/almanac/internal/store"
)

// maxImportBytes caps the uploaded CSV size to guard memory (5 MiB is ample for
// a personal ledger export of tens of thousands of rows).
const maxImportBytes = 5 << 20

// previewRowLimit is how many parsed rows the preview response echoes back for
// the user to eyeball before confirming. The full parse still happens; only the
// echoed sample is capped.
const previewRowLimit = 10

// importRow is one parsed-and-classified CSV line, shared by preview echo and
// confirm input. Amount is the yuan magnitude string (unsigned) so the confirm
// step re-parses it the same way preview did, and RecordTime is already
// normalized to "YYYY-MM-DD HH:mm" CST.
type importRow struct {
	RecordTime string `json:"record_time"` // normalized "YYYY-MM-DD HH:mm"
	RawType    string `json:"raw_type"`    // classification input (子类别优先, else 类别)
	Amount     string `json:"amount"`      // yuan magnitude, unsigned
	Note       string `json:"note"`
	// Preview-only, ignored on confirm (confirm re-classifies server-side):
	CategoryID   *int64 `json:"category_id,omitempty"`
	CategoryName string `json:"category_name,omitempty"` // full path, empty = 待分类
}

// importPreviewResponse summarizes a parsed file: per-row sample plus totals so
// the user can sanity-check before committing. Nothing is written to the DB.
type importPreviewResponse struct {
	Total          int         `json:"total"`           // parsable rows
	Skipped        int         `json:"skipped"`         // rows dropped (bad amount/time)
	Classified     int         `json:"classified"`      // rows a rule matched
	Unclassified   int         `json:"unclassified"`    // rows with no match (待分类)
	IncomeCents    int64       `json:"income_cents"`    // sum of matched income rows
	ExpenseCents   int64       `json:"expense_cents"`   // sum of matched expense rows
	UnclassifiedC  int64       `json:"unclassified_cents"`
	SampleRows     []importRow `json:"sample_rows"`     // first previewRowLimit rows
	SkippedDetails []string    `json:"skipped_details"` // human-readable skip reasons (capped)
}

// csvColumns maps the "毛线记账本" export header to positional indexes, resolved
// by header name so column reordering across export versions is tolerated.
type csvColumns struct {
	date    int // 账单日 -> record_time
	class   int // 类别
	subclas int // 子类别
	amount  int // 金额
	note    int // 备注
}

// resolveColumns finds the needed columns by header name. Missing required
// columns (date/amount and at least one of 类别/子类别) is a hard error.
func resolveColumns(header []string) (csvColumns, error) {
	cols := csvColumns{date: -1, class: -1, subclas: -1, amount: -1, note: -1}
	for i, h := range header {
		switch strings.TrimSpace(h) {
		case "账单日":
			cols.date = i
		case "类别":
			cols.class = i
		case "子类别":
			cols.subclas = i
		case "金额":
			cols.amount = i
		case "备注":
			cols.note = i
		}
	}
	if cols.date < 0 || cols.amount < 0 || (cols.class < 0 && cols.subclas < 0) {
		return cols, errors.New("缺少必需列（需要 账单日、金额，以及 类别 或 子类别）")
	}
	return cols, nil
}

// at safely reads column i from a record, returning "" when out of range.
func at(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

// classifyInput picks the classification string: 子类别优先, 子类别为空则退回类别.
func classifyInput(rec []string, cols csvColumns) string {
	if sub := at(rec, cols.subclas); sub != "" {
		return sub
	}
	return at(rec, cols.class)
}

// slashTimeLayouts are the "毛线记账本" timestamp shapes (slash-separated, no
// timezone). They are interpreted as CST wall-clock and reformatted to the
// canonical "YYYY-MM-DD HH:mm" that normalizeRecordTime already accepts.
var slashTimeLayouts = []string{
	"2006/1/2 15:04",
	"2006/1/2 15:04:05",
}

// normalizeCSVTime accepts the slash format used by the export and falls back to
// the shared normalizeRecordTime for any other shape. Returns the canonical
// "YYYY-MM-DD HH:mm" CST string.
func normalizeCSVTime(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	for _, layout := range slashTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, cstZone); err == nil {
			return t.Format("2006-01-02 15:04"), nil
		}
	}
	return normalizeRecordTime(s)
}

// parseImportCSV reads the whole CSV, resolves columns by header, and returns
// the parsable rows plus skip reasons. Rows with an unparsable amount or time
// are skipped (not fatal); a missing header or zero data rows is a hard error.
// Classification is NOT done here (caller does it with the user id).
func parseImportCSV(r io.Reader) ([]importRow, []string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate ragged rows; we index defensively
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err == io.EOF {
		return nil, nil, errors.New("文件为空")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("读取表头失败: %w", err)
	}
	// Strip a UTF-8 BOM from the first header cell if present.
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	cols, err := resolveColumns(header)
	if err != nil {
		return nil, nil, err
	}

	var rows []importRow
	var skipped []string
	line := 1 // header consumed
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("第 %d 行: 解析失败", line))
			continue
		}
		// Skip fully blank lines silently.
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}

		rawType := classifyInput(rec, cols)
		amountRaw := at(rec, cols.amount)
		if _, perr := parseAmountToCents(amountRaw); perr != nil {
			skipped = append(skipped, fmt.Sprintf("第 %d 行: 金额无效 (%q)", line, amountRaw))
			continue
		}
		recordTime, terr := normalizeCSVTime(at(rec, cols.date))
		if terr != nil {
			skipped = append(skipped, fmt.Sprintf("第 %d 行: 时间无效 (%q)", line, at(rec, cols.date)))
			continue
		}
		if strings.TrimSpace(rawType) == "" {
			skipped = append(skipped, fmt.Sprintf("第 %d 行: 缺少类别/子类别", line))
			continue
		}
		rows = append(rows, importRow{
			RecordTime: recordTime,
			RawType:    rawType,
			Amount:     amountRaw,
			Note:       at(rec, cols.note),
		})
	}
	if len(rows) == 0 && len(skipped) == 0 {
		return nil, nil, errors.New("没有可导入的数据行")
	}
	return rows, skipped, nil
}

// classifyRows runs each row's RawType through the shared routing engine and
// tallies income/expense/unclassified totals. dirByID maps category_id to its
// direction (1 income / -1 expense) so preview totals split correctly. The
// per-row CategoryID/CategoryName are filled for the echoed sample. Returns the
// mutated rows plus a filled-in preview summary (minus sample slicing).
func classifyRows(st *store.Store, userID int64, rows []importRow) importPreviewResponse {
	cats, err := st.ListCategories(userID)
	if err != nil {
		log.Printf("import: list categories failed for user %d: %v", userID, err)
	}
	dirByID := make(map[int64]int, len(cats))
	for _, c := range cats {
		dirByID[c.ID] = c.Direction
	}

	var resp importPreviewResponse
	resp.Total = len(rows)
	for i := range rows {
		cents, _ := parseAmountToCents(rows[i].Amount) // already validated in parse
		catID := st.RouteEntry(userID, cents, rows[i].RawType)
		if catID == nil {
			resp.Unclassified++
			resp.UnclassifiedC += cents
			continue
		}
		rows[i].CategoryID = catID
		if path, perr := st.CategoryPath(userID, *catID); perr == nil {
			rows[i].CategoryName = path
		}
		resp.Classified++
		switch dirByID[*catID] {
		case 1:
			resp.IncomeCents += cents
		case -1:
			resp.ExpenseCents += cents
		}
	}
	return resp
}

// importCSVHandler serves POST /api/import/csv: parse + classify a multipart
// CSV upload and return a preview WITHOUT writing anything to the database.
func importCSVHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
		if err := r.ParseMultipartForm(maxImportBytes); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "上传解析失败（文件过大或格式错误）"})
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "缺少上传文件字段 file"})
			return
		}
		defer file.Close()

		rows, skipped, perr := parseImportCSV(file)
		if perr != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: perr.Error()})
			return
		}

		resp := classifyRows(st, u.ID, rows)
		resp.Skipped = len(skipped)
		resp.SkippedDetails = capStrings(skipped, 20)
		if len(rows) > previewRowLimit {
			resp.SampleRows = rows[:previewRowLimit]
		} else {
			resp.SampleRows = rows
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// capStrings returns at most n items from s (nil-safe).
func capStrings(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// importConfirmResponse reports the committed batch outcome.
type importConfirmResponse struct {
	Status       string `json:"status"`
	Imported     int    `json:"imported"`
	Skipped      int    `json:"skipped"`
	Classified   int    `json:"classified"`
	Unclassified int    `json:"unclassified"`
}

// importConfirmHandler serves POST /api/import/confirm: re-parse the SAME
// multipart CSV file (single source of truth), re-classify every row
// server-side, and commit them in one transaction (all-or-nothing). The file
// is re-uploaded rather than echoing parsed rows back and forth, so the
// committed data is authoritative and payloads stay small.
func importConfirmHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		u := currentUser(st, r)
		if u == nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
		if err := r.ParseMultipartForm(maxImportBytes); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "上传解析失败（文件过大或格式错误）"})
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "缺少上传文件字段 file"})
			return
		}
		defer file.Close()

		rows, skipped, perr := parseImportCSV(file)
		if perr != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: perr.Error()})
			return
		}

		ledgerID, err := st.DefaultLedgerID(u.ID)
		if err != nil {
			log.Printf("import confirm: no default ledger for user %d: %v", u.ID, err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
			return
		}

		entries := make([]store.Entry, 0, len(rows))
		var classified, unclassified int
		for _, row := range rows {
			cents, _ := parseAmountToCents(row.Amount) // validated in parse
			catID := st.RouteEntry(u.ID, cents, row.RawType)
			if catID == nil {
				unclassified++
			} else {
				classified++
			}
			entries = append(entries, store.Entry{
				UserID:      u.ID,
				LedgerID:    ledgerID,
				CategoryID:  catID,
				AmountCents: cents,
				RawType:     row.RawType,
				RecordTime:  row.RecordTime,
				Note:        row.Note,
				Source:      "csv",
			})
		}

		imported, err := st.InsertEntriesTx(entries)
		if err != nil {
			log.Printf("import confirm: batch insert failed for user %d: %v", u.ID, err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "导入失败，已回滚"})
			return
		}
		log.Printf("import: user %d committed %d entries (%d classified, %d unclassified, %d skipped)",
			u.ID, imported, classified, unclassified, len(skipped))
		_ = json.NewEncoder(w).Encode(importConfirmResponse{
			Status:       "ok",
			Imported:     imported,
			Skipped:      len(skipped),
			Classified:   classified,
			Unclassified: unclassified,
		})
	}
}
