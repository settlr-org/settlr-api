package export

import "testing"

func TestCSVSafe(t *testing.T) {
	cases := map[string]string{
		"": "", "plain": "plain", "=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@a": "'@a", "\tx": "'\tx", "\rx": "'\rx",
		"a=b": "a=b", " =x": "' =x", "'=x": "''=x", "\"=x": "'\"=x", "\nx": "'\nx", "  @x": "'  @x", "a b": "a b", " plain": " plain",
	}
	for in, want := range cases {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
