package baidu

import "testing"

// Vectors come from recorded traffic (docs/baidu-api.md).
func TestSignatures(t *testing.T) {
	if got := locateRand("test_bduss", 10086, 1571140066, "O|1E67351CCE80B2CF48DB511CD77ACD9F"); got != "b6bb7a6f46899e181baea58798d4fdb889775c2c" {
		t.Errorf("locateRand = %s", got)
	}
	if got := tiebaDevice["_phone_imei"]; got != "161177100287274" {
		t.Errorf("imei = %s", got)
	}
	if got := tiebaDevice["cuid"]; got != "0C92F544C09528D294B3857079A22950|472782001771161" {
		t.Errorf("cuid = %s", got)
	}
	if got := dataOffset(3241440363, "ea2e4a9d8add947abc90705d59d34a74", 1790356507, 65536); got != 30714 {
		t.Errorf("dataOffset = %d", got)
	}
	if got := dataOffset(1, "x", 1, 100); got != 0 {
		t.Errorf("dataOffset of a small file = %d", got)
	}
}

func TestDeobfuscateMD5(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"d8319ac05p986c5910e98c966b7d1f9d", "d033a1b6d912dfa7e2d6d27211cac9f1"},
		{"d033a1b6d912dfa7e2d6d27211cac9f1", "d033a1b6d912dfa7e2d6d27211cac9f1"}, // not obfuscated
		{"", ""},
	} {
		if got := deobfuscateMD5(c.raw); got != c.want {
			t.Errorf("deobfuscateMD5(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
