package model

import "testing"

func TestBilibiliBareIdentifiers(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"av170001", "https://www.bilibili.com/video/av170001"},
		{"AV000170001?p=2", "https://www.bilibili.com/video/av170001?p=2"},
		{"BV1xx411c7mD?p=3", "https://www.bilibili.com/video/BV1xx411c7mD?p=3"},
	} {
		got, ok := BilibiliURL(tc.input)
		if !ok || got != tc.want {
			t.Fatalf("BilibiliURL(%q)=%q,%v want%q", tc.input, got, ok, tc.want)
		}
		p, ok := Identify(tc.input)
		if !ok || p.ID != "bilibili" {
			t.Fatalf("bare video not identified: %q", tc.input)
		}
	}
	for _, raw := range []string{"av0", "avatar", "fav170001", "av170001extra", "av-1", "av9223372036854775808", "https://evil.example/av170001"} {
		if got, ok := BilibiliURL(raw); ok {
			t.Fatalf("invalid bare identifier accepted: %q -> %q", raw, got)
		}
	}
}
