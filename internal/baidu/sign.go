package baidu

import (
	"crypto/md5"
	"crypto/rc4"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
)

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// downloadSign signs a download request the way the web app does: RC4 of
// sign1 keyed with sign3 (both template variables), in base64.
func downloadSign(sign1, sign3 string) string {
	c, _ := rc4.NewCipher([]byte(sign3))
	out := []byte(sign1)
	c.XORKeyStream(out, out)
	return base64.StdEncoding.EncodeToString(out)
}

// obfuscateMD5 is how md5s travel in Baidu's APIs; deobfuscateMD5 undoes it.
// The block swap is its own inverse; the digits are XORed with their position,
// and the tenth becomes a letter g–v, which marks the value as obfuscated.
func obfuscateMD5(m string) string {
	if len(m) != 32 {
		return m
	}
	o := []byte(m[8:16] + m[0:8] + m[24:32] + m[16:24])
	for i, c := range o {
		o[i] = hexDigit(hexValue(c) ^ (i & 15))
	}
	o[9] = byte('g' + hexValue(o[9]))
	return string(o)
}

// deobfuscateMD5 undoes obfuscateMD5; plain md5s pass through.
func deobfuscateMD5(raw string) string {
	if len(raw) != 32 || strings.ContainsRune("0123456789abcdef", rune(raw[9])) {
		return raw
	}
	s := []byte(raw)
	s[9] = hexDigit(int(raw[9] - 'g'))
	o := make([]byte, 32)
	for i, c := range s {
		o[i] = hexDigit(hexValue(c) ^ (i & 15))
	}
	return string(o[8:16]) + string(o[0:8]) + string(o[24:32]) + string(o[16:24])
}

func hexDigit(v int) byte { return "0123456789abcdef"[v&15] }

func hexValue(c byte) int {
	if c >= 'a' {
		return int(c-'a') + 10
	}
	return int(c - '0')
}

// rapidWindow is the size of the sample instant upload checks.
const rapidWindow = 256 << 10

// rapidOffset picks where in the file the sample for instant upload starts:
// derived from the user, the (obfuscated) md5 and the time, so the uploader
// must really have the content.
func rapidOffset(uk int64, obfuscatedMD5 string, t, size int64) int64 {
	span := size - rapidWindow + 1
	if span <= 1 {
		return 0
	}
	h := md5hex([]byte(strconv.FormatInt(uk, 10) + obfuscatedMD5 + strconv.FormatInt(t, 10)))
	v, _ := strconv.ParseUint(h[:8], 16, 64)
	return int64(v % uint64(span))
}
