package main

import "testing"

func TestSplitCategoryPath(t *testing.T) {
	cases := []struct {
		path, top, sub string
	}{
		{"", "", ""},
		{"餐饮", "餐饮", ""},
		{"餐饮>外卖", "餐饮", "外卖"},
		{"餐饮>饮品>咖啡", "餐饮", "咖啡"},
	}
	for _, c := range cases {
		top, sub := splitCategoryPath(c.path)
		if top != c.top || sub != c.sub {
			t.Errorf("splitCategoryPath(%q) = (%q,%q), want (%q,%q)", c.path, top, sub, c.top, c.sub)
		}
	}
}

func TestCentsToYuanExport(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "0"},
		{100, "1"},
		{1234, "12.34"},
		{1200, "12"},
		{1230, "12.3"},
		{-1234, "12.34"}, // unsigned magnitude
		{5, "0.05"},
	}
	for _, c := range cases {
		if got := centsToYuanExport(c.cents); got != c.want {
			t.Errorf("centsToYuanExport(%d) = %q, want %q", c.cents, got, c.want)
		}
	}
}

func TestDirectionLabel(t *testing.T) {
	cases := map[int]string{1: "收入", -1: "支出", 0: "未分类", 99: "未分类"}
	for d, want := range cases {
		if got := directionLabel(d); got != want {
			t.Errorf("directionLabel(%d) = %q, want %q", d, got, want)
		}
	}
}

func TestURLEncode(t *testing.T) {
	// ASCII unreserved passes through; a space and non-ASCII get percent-encoded.
	if got := urlEncode("a-b_c.d~e"); got != "a-b_c.d~e" {
		t.Errorf("urlEncode unreserved changed: %q", got)
	}
	if got := urlEncode(" "); got != "%20" {
		t.Errorf("urlEncode space = %q, want %%20", got)
	}
}
