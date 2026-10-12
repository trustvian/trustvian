package cirender

import "testing"

func TestInert(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "`plain`"},
		{"", "*(empty)*"},
		{"a|b", "`a\uFF5Cb`"},
		{`a\|b\`, "`a\\\uFF5Cb\\`"},
		{"a`b", "``a`b``"},
		{"`edge`", "`` `edge` ``"},
		{"a\nb", "`a\uFFFDb`"},
		{"\u202Eevil", "`\uFFFDevil`"},
		{"bad\xffbyte", "`bad\uFFFDbyte`"},
		{"   ", "*(blank)*"},
		{"\u3164", "`\uFFFD`"},
		{"a\u2800b", "`a\uFFFDb`"},
	}
	for _, tt := range tests {
		if got := inert(tt.in, 64); got != tt.want {
			t.Errorf("inert(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := inert("abcdefgh", 6); got != "`abc…`"+truncatedMarker {
		t.Errorf("truncation: %q", got)
	}
	if got := inert("ééé", 4); got != "`…`"+truncatedMarker { // é would make 5 bytes
		t.Errorf("truncation splits a rune or exceeds the limit: %q", got)
	}
	if got := inert("éééé", 7); got != "`éé…`"+truncatedMarker {
		t.Errorf("truncation: %q", got)
	}
}
