package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mutouyun/almanac/internal/store"
)

// exportPageSize is how many rows we pull per ListEntries call while streaming
// the export. Kept at the store's max page size so we make the fewest possible
// round-trips without ever holding the whole ledger in memory at once.
const exportPageSize = 200

// exportColumns is the CSV header. It is a superset of the 毛线记账本 import
// format (账单日/类别/子类别/金额/备注) so an exported file can be re-imported
// directly, plus 方向/来源 for human readability (the importer ignores unknown
// columns and resolves the ones it needs by header name).
var exportColumns = []string{"账单日", "类别", "子类别", "金额", "方向", "来源", "备注"}

// splitCategoryPath turns a "root>child>leaf" path into (root, leaf) for the
// 类别/子类别 columns. A single-level path yields (name, "") and an empty path
// yields ("", ""). Middle levels are dropped: personal ledgers rarely nest past
// two levels, and the importer only reads 子类别 (falling back to 类别) anyway,
// so a round-trip re-classifies correctly.
func splitCategoryPath(path string) (top, sub string) {
	if path == "" {
		return "", ""
	}
	parts := strings.Split(path, ">")
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[len(parts)-1]
}

// directionLabel maps the numeric direction to a Chinese label for the export.
func directionLabel(d int) string {
	switch d {
	case 1:
		return "收入"
	case -1:
		return "支出"
	default:
		return "未分类"
	}
}

// exportEntriesHandler serves GET /api/entries/export: stream every entry that
// matches the same filters as the list view as a UTF-8 CSV attachment. Rows are
// paged from the store so memory stays bounded regardless of ledger size, and a
// UTF-8 BOM is written first so Excel opens the file without mojibake.
func exportEntriesHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		u := currentUser(st, r)
		if u == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "unauthorized"})
			return
		}

		filter := buildEntryFilter(st, r, u)

		filename := fmt.Sprintf("almanac-账本-%s.csv", time.Now().In(cstZone).Format("20060102-150405"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"almanac-ledger.csv\"; filename*=UTF-8''%s", urlEncode(filename)))

		// UTF-8 BOM so Excel detects the encoding.
		if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
			return
		}

		cw := csv.NewWriter(w)
		if err := cw.Write(exportColumns); err != nil {
			log.Printf("export: write header failed for user %d: %v", u.ID, err)
			return
		}

		offset := 0
		written := 0
		for {
			entries, _, err := st.ListEntries(u.ID, filter, exportPageSize, offset)
			if err != nil {
				log.Printf("export: list entries failed for user %d at offset %d: %v", u.ID, offset, err)
				// Flush what we have; a partial file beats a 500 mid-stream.
				break
			}
			if len(entries) == 0 {
				break
			}
			for _, e := range entries {
				top, sub := splitCategoryPath(e.CategoryPath)
				if top == "" && e.CategoryName != "" {
					top = e.CategoryName
				}
				rec := []string{
					e.RecordTime,
					top,
					sub,
					centsToYuanExport(e.AmountCents),
					directionLabel(e.Direction),
					e.Source,
					e.Note,
				}
				if err := cw.Write(rec); err != nil {
					log.Printf("export: write row failed for user %d: %v", u.ID, err)
					cw.Flush()
					return
				}
			}
			written += len(entries)
			if len(entries) < exportPageSize {
				break
			}
			offset += exportPageSize
		}

		cw.Flush()
		if err := cw.Error(); err != nil {
			log.Printf("export: flush failed for user %d: %v", u.ID, err)
			return
		}
		log.Printf("export: user %d exported %d entries", u.ID, written)
	}
}

// centsToYuanExport formats unsigned cents as a plain yuan magnitude string
// ("12.34", trailing-zero trimmed to at most 2 decimals). Unsigned to match the
// importer, which derives direction from the category, not the amount sign.
func centsToYuanExport(cents int64) string {
	if cents < 0 {
		cents = -cents
	}
	yuan := cents / 100
	frac := cents % 100
	if frac == 0 {
		return fmt.Sprintf("%d", yuan)
	}
	return strings.TrimRight(fmt.Sprintf("%d.%02d", yuan, frac), "0")
}

// urlEncode percent-encodes a string for the RFC 5987 filename* parameter so
// non-ASCII filenames survive the Content-Disposition header.
func urlEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}
