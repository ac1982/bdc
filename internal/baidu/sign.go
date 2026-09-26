package baidu

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
)

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha1hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// devUID identifies this "device" to the download API; derived from BDUSS.
func devUID(bduss string) string {
	return strings.ToUpper(md5hex(bduss)) + "|0"
}

// locateSecret is the netdisk client's constant for signing locatedownload.
const locateSecret = "ebrcUYiuxaZv2XGu7KIYKxUrqfnOfpDF"

// locateRand signs a locatedownload request made at unix time t.
func locateRand(bduss string, uid uint64, t int64, devuid string) string {
	return sha1hex(sha1hex(bduss) + strconv.FormatUint(uid, 10) + locateSecret + strconv.FormatInt(t, 10) + devuid)
}

// tiebaSign signs tieba client requests: md5 of the sorted raw key=value
// pairs followed by a constant.
func tiebaSign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + params[k])
	}
	return strings.ToUpper(md5hex(b.String() + "tiebaclient!!!"))
}

// tiebaDevice is the fixed phone the tieba login pretends to be.
var tiebaDevice = func() map[string]string {
	const model, version, from = "LG-H818", "7.0.0.0", "mini_ad_wandoujia"
	h := uint64(53202347234687234)
	for _, c := range []byte(model + "_") {
		h += h<<5 + uint64(c)
	}
	h %= 1e15
	if h < 1e14 {
		h += 1e14
	}
	imei := strconv.FormatUint(h, 10)
	rev := []byte(imei)
	slices.Reverse(rev)
	return map[string]string{
		"_client_type":    "2",
		"_client_version": version,
		"_phone_imei":     imei,
		"from":            from,
		"model":           model,
		"cuid":            strings.ToUpper(md5hex("_"+version+"_"+imei+"_"+from)) + "|" + string(rev),
	}
}()

// realMD5 undoes the obfuscation Baidu applies to md5 fields. For a file
// stored as one block the block's md5 is the file's md5.
func realMD5(raw string, blocks []string) string {
	if len(blocks) == 1 {
		return blocks[0]
	}
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

// dataOffset picks the 4 KiB window of a file that precreate uses to check
// the uploader really has the content.
func dataOffset(uk int64, contentMD5 string, t int64, size int64) int64 {
	span := size - 4096 + 1
	if span <= 1 {
		return 0
	}
	h := md5hex(strconv.FormatInt(uk, 10) + contentMD5 + strconv.FormatInt(t, 10))
	v, _ := strconv.ParseUint(h[:8], 16, 64)
	return int64(v % uint64(span))
}
