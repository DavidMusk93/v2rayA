package service

import "testing"

func TestSameRelease(t *testing.T) {
	ok := [][2]string{
		{"2.5.8", "2.5.8"},
		{"2.5.8", "v2.5.8"},
		{"2.5.8", "v2.5.8-3-g2186d1a"},
		{"v2.5.8", "v2.5.8-3-g2186d1a-dirty"},
		{"2.5.8-1-gabcdef0", "v2.5.8-3-g2186d1a"},
	}
	for _, pair := range ok {
		if !sameRelease(pair[0], pair[1]) {
			t.Errorf("sameRelease(%q, %q) = false", pair[0], pair[1])
		}
	}
	bad := [][2]string{
		{"26.9.30", "v2.5.8-3-g2186d1a"},
		{"2.5.9", "v2.5.8-3-g2186d1a"},
		{"2.5.80", "2.5.8"},
		{"2.5.8", "v2.5.8-3-gzzzz"},
		{"", "2.5.8"},
		{"2.5.8", ""},
	}
	for _, pair := range bad {
		if sameRelease(pair[0], pair[1]) {
			t.Errorf("sameRelease(%q, %q) = true", pair[0], pair[1])
		}
	}
}
