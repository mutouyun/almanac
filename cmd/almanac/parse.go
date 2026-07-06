package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// errZeroAmount is returned when the parsed amount is exactly zero.
var errZeroAmount = errors.New("amount must not be zero")

// parseAmountToCents converts a decimal money string (yuan) into UNSIGNED
// integer cents WITHOUT ever going through float64, to avoid IEEE-754
// truncation (e.g. -19.9 -> -18.999...). It accepts an optional leading sign,
// an integer part, and up to two fractional digits. A third fractional digit
// (if present) is used for half-up rounding; more than three digits are
// rejected as too precise for currency.
//
// The sign is deliberately IGNORED (a legacy client may still send "-19.9"):
// direction is no longer carried by the amount's sign but derived from the
// matched category, so amounts are stored as unsigned absolute cents.
//
// Examples: "-19.9" -> 1990, "100" -> 10000, "0.005" -> 1 (rounds up).
func parseAmountToCents(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("empty amount")
	}

	// Drop an optional leading sign; magnitude only.
	switch s[0] {
	case '+', '-':
		s = s[1:]
	}
	if s == "" {
		return 0, errors.New("invalid amount: sign only")
	}

	intPart, fracPart := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		intPart = s[:dot]
		fracPart = s[dot+1:]
	}
	if intPart == "" {
		intPart = "0"
	}

	// Validate digit-only parts.
	if !isDigits(intPart) || (fracPart != "" && !isDigits(fracPart)) {
		return 0, fmt.Errorf("invalid amount: %q", raw)
	}
	if len(fracPart) > 3 {
		return 0, fmt.Errorf("amount has too many decimal places: %q", raw)
	}

	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer part: %w", err)
	}

	// Normalize fractional part to exactly 3 digits: [cents(2)][rounding(1)].
	frac3 := (fracPart + "000")[:3]
	centsDigits, _ := strconv.ParseInt(frac3[:2], 10, 64)
	roundDigit := frac3[2] - '0'

	cents := whole*100 + centsDigits
	if roundDigit >= 5 {
		cents++ // half-up
	}
	if cents == 0 {
		return 0, errZeroAmount
	}
	return cents, nil
}

// isDigits reports whether s is non-empty and all ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// normalizeRecordTime parses a timestamp and returns the wall-clock time in
// China Standard Time (UTC+8), formatted as the fixed-length
// "YYYY-MM-DD HH:mm". Seconds are truncated, not rounded.
//
// It accepts two shapes:
//   - RFC3339 with a timezone offset (e.g. "2026-07-05T14:30:00+08:00"), used
//     by webhook callers; the instant is converted into CST.
//   - A bare CST wall-clock time without offset (e.g. "2026-07-06 12:30" or
//     "2026-07-06T12:30"), used by the manual-entry UI; it is interpreted as
//     already being in CST.
func normalizeRecordTime(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("empty time")
	}
	// Preferred: RFC3339 with offset (webhook path).
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(cstZone).Format("2006-01-02 15:04"), nil
	}
	// Fallback: bare wall-clock time from the manual-entry UI. Both a space and
	// a 'T' separator are accepted, with or without seconds. Parsed in CST.
	for _, layout := range []string{
		"2006-01-02 15:04",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, s, cstZone); err == nil {
			return t.Format("2006-01-02 15:04"), nil
		}
	}
	return "", fmt.Errorf("invalid time %q (want RFC3339 like 2026-07-05T14:30:00+08:00 or wall-clock like 2026-07-06 12:30): %w", raw, errInvalidTime)
}

// errInvalidTime is a sentinel wrapped by normalizeRecordTime for callers that
// only care that parsing failed, not the specific layout mismatch.
var errInvalidTime = errors.New("unrecognized time format")
