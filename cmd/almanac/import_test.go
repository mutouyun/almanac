package main

import (
	"strings"
	"testing"
)

// sampleCSV mirrors the "毛线记账本" export: a single header then data rows,
// UTF-8, slash-formatted times, unsigned amounts.
const sampleCSV = `账单日,账本,类别,子类别,金额,备注,创建时间
2026/6/29 21:46,日常支出,生活日用,生活日用,9.9,,2026/6/29 21:46
2026/6/29 19:07,日常支出,饮食,餐食,39,聚餐,2026/6/29 19:07
2026/6/27 16:15,日常支出,其他,,450,,2026/6/27 16:15
`

func TestParseImportCSV(t *testing.T) {
	rows, skipped, err := parseImportCSV(strings.NewReader(sampleCSV))
	if err != nil {
		t.Fatalf("parseImportCSV: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none", skipped)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}

	// Row 0: subcategory present -> used as RawType; time reformatted.
	if rows[0].RawType != "生活日用" {
		t.Errorf("row0 RawType = %q, want 生活日用", rows[0].RawType)
	}
	if rows[0].RecordTime != "2026-06-29 21:46" {
		t.Errorf("row0 RecordTime = %q, want 2026-06-29 21:46", rows[0].RecordTime)
	}
	if rows[0].Amount != "9.9" {
		t.Errorf("row0 Amount = %q, want 9.9", rows[0].Amount)
	}

	// Row 1: note carried through.
	if rows[1].RawType != "餐食" {
		t.Errorf("row1 RawType = %q, want 餐食 (subcategory priority)", rows[1].RawType)
	}
	if rows[1].Note != "聚餐" {
		t.Errorf("row1 Note = %q, want 聚餐", rows[1].Note)
	}

	// Row 2: empty subcategory -> falls back to 类别.
	if rows[2].RawType != "其他" {
		t.Errorf("row2 RawType = %q, want 其他 (fallback to 类别)", rows[2].RawType)
	}
}

func TestParseImportCSVSkipsBadRows(t *testing.T) {
	csv := `账单日,账本,类别,子类别,金额,备注,创建时间
2026/6/29 21:46,日常支出,饮食,餐食,abc,,2026/6/29 21:46
not-a-date,日常支出,饮食,餐食,39,,x
2026/6/29 19:07,日常支出,饮食,餐食,39,,2026/6/29 19:07
`
	rows, skipped, err := parseImportCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parseImportCSV: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("rows = %d, want 1 (two bad rows skipped)", len(rows))
	}
	if len(skipped) != 2 {
		t.Errorf("skipped = %d, want 2", len(skipped))
	}
}

func TestParseImportCSVMissingColumns(t *testing.T) {
	csv := `foo,bar,baz
1,2,3
`
	if _, _, err := parseImportCSV(strings.NewReader(csv)); err == nil {
		t.Error("expected error for missing required columns")
	}
}

func TestParseImportCSVStripsBOM(t *testing.T) {
	csv := "\ufeff账单日,账本,类别,子类别,金额,备注,创建时间\n2026/6/29 21:46,日常支出,饮食,餐食,39,,2026/6/29 21:46\n"
	rows, _, err := parseImportCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parseImportCSV with BOM: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
}

func TestNormalizeCSVTime(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"2026/6/29 21:46", "2026-06-29 21:46", false},
		{"2026/12/1 9:05", "2026-12-01 09:05", false},
		{"2026/6/29 21:46:30", "2026-06-29 21:46", false},
		{"2026-06-29 21:46", "2026-06-29 21:46", false}, // canonical fallback
		{"garbage", "", true},
	}
	for _, c := range cases {
		got, err := normalizeCSVTime(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeCSVTime(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeCSVTime(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeCSVTime(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
