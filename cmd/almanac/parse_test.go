package main

import "testing"

func TestParseAmountToCents(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"-19.9", 1990, false}, // sign dropped: amount is stored unsigned (abs)
		{"19.9", 1990, false},
		{"19.90", 1990, false},
		{"100", 10000, false},
		{"0.01", 1, false},
		{"-0.01", 1, false}, // sign dropped: abs value
		{"+42.5", 4250, false},
		{"1234.56", 123456, false},
		{"0.005", 1, false},   // half-up rounds to 1 cent
		{"0.004", 0, true},    // rounds to 0 -> zero amount error
		{"0", 0, true},        // zero
		{"0.00", 0, true},     // zero
		{"-0", 0, true},       // zero
		{"9.999", 1000, false}, // rounds up to 1000 cents
		{"", 0, true},         // empty
		{"abc", 0, true},      // non-numeric
		{"1.2.3", 0, true},    // malformed
		{"1.234", 123, false},  // 3rd decimal used for rounding: 4<5 -> down
		{"1.235", 124, false},  // 3rd decimal 5 -> half-up
		{"1.2345", 0, true},    // 4 decimals -> too precise
		{"-", 0, true},        // sign only
		{"12.3", 1230, false},
	}
	for _, c := range cases {
		got, err := parseAmountToCents(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseAmountToCents(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseAmountToCents(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseAmountToCents(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestNormalizeRecordTime(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"2026-07-05T14:30:00+08:00", "2026-07-05 14:30", false},
		{"2026-07-05T14:30:45+08:00", "2026-07-05 14:30", false}, // seconds truncated
		{"2026-07-05T06:30:00+00:00", "2026-07-05 14:30", false}, // UTC -> CST +8
		{"2026-07-05T14:30:00-05:00", "2026-07-06 03:30", false}, // EST -> CST
		{"", "", true},
		{"not-a-time", "", true},
		{"2026-07-05 14:30", "", true}, // not RFC3339
	}
	for _, c := range cases {
		got, err := normalizeRecordTime(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeRecordTime(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeRecordTime(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeRecordTime(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
