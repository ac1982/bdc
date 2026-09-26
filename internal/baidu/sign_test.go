package baidu

import "testing"

func TestObfuscateMD5(t *testing.T) {
	// A pair observed in real traffic (docs/baidu-api.md).
	const plain, obf = "d033a1b6d912dfa7e2d6d27211cac9f1", "d8319ac05p986c5910e98c966b7d1f9d"
	if got := obfuscateMD5(plain); got != obf {
		t.Errorf("obfuscateMD5 = %s", got)
	}
	if got := deobfuscateMD5(obf); got != plain {
		t.Errorf("deobfuscateMD5 = %s", got)
	}
	if got := deobfuscateMD5(plain); got != plain {
		t.Errorf("a plain md5 changed: %s", got)
	}
	for _, m := range []string{"00000000000000000000000000000000", "ffffffffffffffffffffffffffffffff"} {
		if deobfuscateMD5(obfuscateMD5(m)) != m {
			t.Errorf("round trip of %s", m)
		}
	}
}

func TestDownloadSign(t *testing.T) {
	// RC4("Key", "Plaintext") = bbf316e8d940af0ad3 (the classic test vector).
	if got := downloadSign("Plaintext", "Key"); got != "u/MW6NlArwrT" {
		t.Errorf("downloadSign = %s", got)
	}
}

func TestRapidOffset(t *testing.T) {
	if got := rapidOffset(1, "x", 1, 100); got != 0 {
		t.Errorf("a file smaller than the window: %d", got)
	}
	if got := rapidOffset(3241440363, "abc", 1790415340, 1<<20); got < 0 || got > 1<<20-rapidWindow {
		t.Errorf("offset %d outside the file", got)
	}
}
