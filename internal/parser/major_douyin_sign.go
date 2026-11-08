// SPDX-License-Identifier: MIT
// Adapted from ParseHub, Copyright (c) 2024 梓澪; full MIT notice in major_common.go.
package parser

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"math/bits"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const majorDouyinUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36 Edg/122.0.0.0"

// SM3 is the hash used by Douyin's browser a_bogus signature.
func majorSM3(input []byte) []byte {
	size := len(input)
	n := (size + 9 + 63) / 64 * 64
	padded := make([]byte, n)
	copy(padded, input)
	padded[size] = 128
	binary.BigEndian.PutUint64(padded[n-8:], uint64(size)*8)
	state := [8]uint32{0x7380166f, 0x4914b2b9, 0x172442d7, 0xda8a0600, 0xa96f30bc, 0x163138aa, 0xe38dee4d, 0xb0fb0e4e}
	for offset := 0; offset < n; offset += 64 {
		var w [68]uint32
		for j := range 16 {
			w[j] = binary.BigEndian.Uint32(padded[offset+j*4:])
		}
		for j := 16; j < 68; j++ {
			x := w[j-16] ^ w[j-9] ^ bits.RotateLeft32(w[j-3], 15)
			w[j] = x ^ bits.RotateLeft32(x, 15) ^ bits.RotateLeft32(x, 23) ^ bits.RotateLeft32(w[j-13], 7) ^ w[j-6]
		}
		a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3], state[4], state[5], state[6], state[7]
		for j := range 64 {
			t := uint32(0x79cc4519)
			ff := a ^ b ^ c
			gg := e ^ f ^ g
			if j >= 16 {
				t = 0x7a879d8a
				ff = (a & b) | (a & c) | (b & c)
				gg = (e & f) | (^e & g)
			}
			aa := bits.RotateLeft32(a, 12)
			ss1 := bits.RotateLeft32(aa+e+bits.RotateLeft32(t, j), 7)
			ss2 := ss1 ^ aa
			tt1 := ff + d + ss2 + (w[j] ^ w[j+4])
			tt2 := gg + h + ss1 + w[j]
			d = c
			c = bits.RotateLeft32(b, 9)
			b = a
			a = tt1
			h = g
			g = bits.RotateLeft32(f, 19)
			f = e
			e = tt2 ^ bits.RotateLeft32(tt2, 9) ^ bits.RotateLeft32(tt2, 17)
		}
		state[0] ^= a
		state[1] ^= b
		state[2] ^= c
		state[3] ^= d
		state[4] ^= e
		state[5] ^= f
		state[6] ^= g
		state[7] ^= h
	}
	out := make([]byte, 32)
	for i, v := range state {
		binary.BigEndian.PutUint32(out[i*4:], v)
	}
	return out
}
func majorRC4(key, input []byte) []byte {
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		return nil
	}
	out := make([]byte, len(input))
	cipher.XORKeyStream(out, input)
	return out
}
func majorDouyinABogus(query string, start, end int64, random [6]byte) string {
	params := majorSM3(majorSM3([]byte(query + "cus")))
	method := majorSM3(majorSM3([]byte("GETcus")))
	ua := [32]byte{76, 98, 15, 131, 97, 245, 224, 133, 122, 199, 241, 166, 79, 34, 90, 191, 128, 126, 122, 98, 66, 11, 14, 40, 49, 110, 110, 173, 67, 96, 138, 252}
	browser := "1536|742|1536|864|0|0|0|0|1536|864|1536|864|1536|742|24|24|MacIntel"
	body := []byte{44, byte(end >> 24), 0, 0, 0, 0, 24, params[21], method[21], 0, ua[23], byte(end >> 16), 0, 0, 0, 1, 0, 239, params[22], method[22], ua[24], byte(end >> 8), 0, 0, 0, 0, byte(end), 0, 0, 14, byte(start >> 24), byte(start >> 16), 0, byte(start >> 8), byte(start), 3, byte(end >> 32), 1, byte(start >> 32), 1, byte(len(browser)), 0, 0, 0}
	checksum := byte(0)
	for _, b := range body {
		checksum ^= b
	}
	body = append(body, []byte(browser)...)
	body = append(body, checksum)
	prefix := []byte{random[0]&170 | 1, random[0]&85 | 2, random[1]&170 | 5, random[1]&85 | 40, random[2]&170 | 1, random[2] & 85, random[3] & 170, random[3] & 85, random[4]&170 | 1, random[4] & 85, random[5]&170 | 5, random[5] & 85}
	payload := append(prefix, majorRC4([]byte("y"), body)...)
	return base64.NewEncoding("Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe").EncodeToString(payload)
}
func majorDouyinQuote(s string) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '*' || c == '-' || c == '.' || c == '_' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
func majorDouyinEncodePairs(pairs [][2]string) string {
	var b strings.Builder
	for i, kv := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(majorDouyinQuote(kv[0]))
		b.WriteByte('=')
		b.WriteString(majorDouyinQuote(kv[1]))
	}
	return b.String()
}
func majorDouyinSignedQuery(params url.Values, cookie string) (string, map[string]string, error) {
	order := []string{
		"device_platform", "aid", "channel", "pc_client_type", "version_code", "version_name",
		"cookie_enabled", "screen_width", "screen_height", "browser_language", "browser_platform",
		"browser_name", "browser_version", "browser_online", "engine_name", "engine_version",
		"os_name", "os_version", "cpu_core_num", "device_memory", "platform", "downlink",
		"effective_type", "aweme_id", "msToken",
	}
	seen := make(map[string]bool, len(order))
	pairs := make([][2]string, 0, len(params)+6)
	for _, k := range order {
		if v := params.Get(k); v != "" {
			pairs = append(pairs, [2]string{k, v})
			seen[k] = true
		}
	}
	extra := make([]string, 0, len(params))
	for k := range params {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		pairs = append(pairs, [2]string{k, params.Get(k)})
	}
	baseQuery := majorDouyinEncodePairs(pairs)
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, err
	}
	start := time.Now().UnixMilli()
	pairs = append(pairs, [2]string{"a_bogus", majorDouyinABogus(baseQuery, start, start+6, random)})
	if fp := majorCookie(cookie, "s_v_web_id"); fp != "" {
		pairs = append(pairs, [2]string{"verifyFp", fp}, [2]string{"fp", fp})
	}
	uifid := ""
	for _, name := range []string{"uifid", "uifid_temp", "uifidtemp", "UIFID", "UIFID_TEMP", "UIFIDTEMP"} {
		if uifid = majorCookie(cookie, name); uifid != "" {
			break
		}
	}
	h := map[string]string{}
	if uifid != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		pairs = append(pairs, [2]string{"uifid", uifid}, [2]string{"timestamp", timestamp})
		query := majorDouyinEncodePairs(pairs)
		sum := md5.Sum([]byte(uifid + "_" + timestamp + "_A96D855A08C0A9707F8BEF0D9A527E4E_" + query))
		signature := hex.EncodeToString(sum[:])
		h["uifid"] = uifid
		h["x-secsdk-web-signature"] = signature
		h["x-secsdk-web-expire"] = timestamp
		return query + "&x-secsdk-web-signature=" + signature, h, nil
	}
	return majorDouyinEncodePairs(pairs), h, nil
}

func majorDouyinSimonEnc(pt [2]uint64, k [4]uint64) [2]uint64 {
	var key [72]uint64
	copy(key[:4], k[:])
	const z = uint64(0x3DC94C3A046D678B)
	for i := 4; i < 72; i++ {
		tmp := bits.RotateLeft64(key[i-1], -3) ^ key[i-3]
		tmp ^= bits.RotateLeft64(tmp, -1)
		bit := (z >> uint((i-4)%62)) & 1
		key[i] = ^key[i-4] ^ tmp ^ bit ^ 3
	}
	x0, x1 := pt[0], pt[1]
	for i := range 72 {
		f := bits.RotateLeft64(x1, 1) & bits.RotateLeft64(x1, 8)
		x0, x1 = x1, x0^f^bits.RotateLeft64(x1, 2)^key[i]
	}
	return [2]uint64{x0, x1}
}

func majorDouyinLadon(unix int64, lcID, aid int, randomBytes [4]byte) string {
	data := []byte(fmt.Sprintf("%d-%d-%d", unix, lcID, aid))
	keygen := append(append([]byte(nil), randomBytes[:]...), strconv.Itoa(aid)...)
	sum := md5.Sum(keygen)
	md5hex := []byte(hex.EncodeToString(sum[:]))
	var hashTable [288]byte
	copy(hashTable[:32], md5hex)
	temp := make([]uint64, 0, 36)
	for i := range 4 {
		temp = append(temp, binary.LittleEndian.Uint64(hashTable[i*8:(i+1)*8]))
	}
	b0, b8 := temp[0], temp[1]
	temp = temp[2:]
	for i := range uint64(0x22) {
		x8 := bits.RotateLeft64(b8, -8) + b0
		x8 ^= i
		temp = append(temp, x8)
		x8 ^= bits.RotateLeft64(b0, -61)
		binary.LittleEndian.PutUint64(hashTable[(i+1)*8:(i+2)*8], x8)
		b0 = x8
		b8 = temp[0]
		temp = temp[1:]
	}
	padLen := 16 - (len(data) % 16)
	padded := make([]byte, len(data)+padLen)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	out := make([]byte, 4+len(padded))
	copy(out[:4], randomBytes[:])
	for blk := 0; blk < len(padded); blk += 16 {
		d0 := binary.LittleEndian.Uint64(padded[blk : blk+8])
		d1 := binary.LittleEndian.Uint64(padded[blk+8 : blk+16])
		for i := range 0x22 {
			h := binary.LittleEndian.Uint64(hashTable[i*8 : (i+1)*8])
			d1 = h ^ (d0 + bits.RotateLeft64(d1, -8))
			d0 = d1 ^ bits.RotateLeft64(d0, -0x3D)
		}
		binary.LittleEndian.PutUint64(out[4+blk:4+blk+8], d0)
		binary.LittleEndian.PutUint64(out[4+blk+8:4+blk+16], d1)
	}
	return base64.StdEncoding.EncodeToString(out)
}

func majorDouyinGorgon(params, payload, cookie string, unix int64, version int, randCE [2]byte) map[string]string {
	var paramList [20]byte
	pSum := md5.Sum([]byte(params))
	copy(paramList[0:4], pSum[:4])
	if payload != "" {
		dSum := md5.Sum([]byte(payload))
		copy(paramList[4:8], dSum[:4])
	}
	if cookie != "" {
		cSum := md5.Sum([]byte(cookie))
		copy(paramList[8:12], cSum[:4])
	}
	binary.BigEndian.PutUint32(paramList[16:20], uint32(unix))
	ticket := strconv.FormatInt(unix*1000, 10)
	khronos := strconv.FormatInt(unix, 10)
	if version == 8404 {
		paramList[12], paramList[13], paramList[14], paramList[15] = 0x01, 0x01, 0x02, 0x04
		prefix := fmt.Sprintf("8404%02x%02x0000", randCE[1]&0xf0, randCE[0])
		return map[string]string{
			"x-ss-req-ticket": ticket,
			"x-khronos":       khronos,
			"x-gorgon":        prefix + hex.EncodeToString(paramList[:]),
		}
	}
	paramList[12], paramList[13], paramList[14], paramList[15] = 0x00, 0x06, 0x0B, 0x1C
	key := [20]byte{0xDF, 0x77, 0xB9, 0x40, 0xB9, 0x9B, 0x84, 0x83, 0xD1, 0xB9, 0xCB, 0xD1, 0xF7, 0xC2, 0xB9, 0x85, 0xC3, 0xD0, 0xFB, 0xC3}
	var eor [20]byte
	for i := range 20 {
		eor[i] = paramList[i] ^ key[i]
	}
	for i := range 20 {
		c := (eor[i] << 4) | (eor[i] >> 4)
		d := eor[(i+1)%20]
		f := bits.Reverse8(c ^ d)
		eor[i] = (f ^ 0xFF) ^ 0x14
	}
	return map[string]string{
		"x-ss-req-ticket": ticket,
		"x-khronos":       khronos,
		"x-gorgon":        "0404b0d30000" + hex.EncodeToString(eor[:]),
	}
}

func majorDouyinAppendVarint(b []byte, field int, val uint64) []byte {
	b = majorDouyinRawVarint(b, uint64(field<<3))
	return majorDouyinRawVarint(b, val)
}
func majorDouyinRawVarint(b []byte, val uint64) []byte {
	v := val & 0xFFFFFFFF
	for v > 0x80 {
		b = append(b, byte((v&0x7F)|0x80))
		v >>= 7
	}
	return append(b, byte(v&0x7F))
}
func majorDouyinAppendBytes(b []byte, field int, data []byte) []byte {
	b = majorDouyinRawVarint(b, uint64((field<<3)|2))
	b = majorDouyinRawVarint(b, uint64(len(data)))
	return append(b, data...)
}
func majorDouyinArgusProtobuf(aid, licenseID, platform int, deviceID, versionName, sdkVersionStr string, sdkVersionInt int, timestamp int64, randVal uint32, bodyHash, queryHash []byte, secDeviceID string) []byte {
	var f15 []byte
	f15 = majorDouyinAppendVarint(f15, 1, 1)
	f15 = majorDouyinAppendVarint(f15, 2, 1)
	f15 = majorDouyinAppendVarint(f15, 3, 1)
	f15 = majorDouyinAppendVarint(f15, 7, 3348294860)
	var f23 []byte
	f23 = majorDouyinAppendBytes(f23, 1, []byte("NX551J"))
	f23 = majorDouyinAppendVarint(f23, 2, 8196)
	f23 = majorDouyinAppendVarint(f23, 4, 2162219008)
	var pb []byte
	pb = majorDouyinAppendVarint(pb, 1, 0x20200929<<1)
	pb = majorDouyinAppendVarint(pb, 2, 2)
	pb = majorDouyinAppendVarint(pb, 3, uint64(randVal))
	pb = majorDouyinAppendBytes(pb, 4, []byte(strconv.Itoa(aid)))
	pb = majorDouyinAppendBytes(pb, 5, []byte(deviceID))
	pb = majorDouyinAppendBytes(pb, 6, []byte(strconv.Itoa(licenseID)))
	pb = majorDouyinAppendBytes(pb, 7, []byte(versionName))
	pb = majorDouyinAppendBytes(pb, 8, []byte(sdkVersionStr))
	pb = majorDouyinAppendVarint(pb, 9, uint64(sdkVersionInt))
	pb = majorDouyinAppendBytes(pb, 10, make([]byte, 8))
	pb = majorDouyinAppendVarint(pb, 11, uint64(platform))
	pb = majorDouyinAppendVarint(pb, 12, uint64(timestamp<<1))
	pb = majorDouyinAppendBytes(pb, 13, bodyHash)
	pb = majorDouyinAppendBytes(pb, 14, queryHash)
	pb = majorDouyinAppendBytes(pb, 15, f15)
	pb = majorDouyinAppendBytes(pb, 16, []byte(secDeviceID))
	pb = majorDouyinAppendBytes(pb, 20, []byte("none"))
	pb = majorDouyinAppendVarint(pb, 21, 738)
	pb = majorDouyinAppendBytes(pb, 23, f23)
	pb = majorDouyinAppendVarint(pb, 25, 2)
	return pb
}
func majorDouyinPKCS7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - (len(data) % blockSize)
	return append(append([]byte(nil), data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
}
func majorDouyinArgusEncrypt(pb []byte) string {
	padded := majorDouyinPKCS7Pad(pb, 16)
	signKey := []byte("\xac\x1a\xda\xae\x95\xa7\xaf\x94\xa5\x11J\xb3\xb3\xa9}\xd8\x00P\xaa\n91L@R\x8c\xae\xc9RV\xc2\x8c")
	sm3Out := []byte("\xfcx\xe0\xa9ez\x0ct\x8c\xe5\x15Y\x90<\xcf\x03Q\x0eQ\xd3\xcf\xf22\xd7\x13C\xe8\x8a2\x1cS\x04")
	var k [4]uint64
	for i := range 4 {
		k[i] = binary.LittleEndian.Uint64(sm3Out[i*8 : (i+1)*8])
	}
	encPB := make([]byte, len(padded))
	for off := 0; off < len(padded); off += 16 {
		pt := [2]uint64{
			binary.LittleEndian.Uint64(padded[off : off+8]),
			binary.LittleEndian.Uint64(padded[off+8 : off+16]),
		}
		ct := majorDouyinSimonEnc(pt, k)
		binary.LittleEndian.PutUint64(encPB[off:off+8], ct[0])
		binary.LittleEndian.PutUint64(encPB[off+8:off+16], ct[1])
	}
	combined := append([]byte("\xf2\xf7\xfc\xff\xf2\xf7\xfc\xff"), encPB...)
	for i := 8; i < len(combined); i++ {
		combined[i] ^= combined[i%8]
	}
	for i, j := 0, len(combined)-1; i < j; i, j = i+1, j-1 {
		combined[i], combined[j] = combined[j], combined[i]
	}
	bBuf := append([]byte("\xa6n\xad\x9fw\x01\xd0\x0c\x18"), combined...)
	bBuf = append(bBuf, 'a', 'o')
	bBufPadded := majorDouyinPKCS7Pad(bBuf, 16)
	aesKey := md5.Sum(signKey[:16])
	aesIV := md5.Sum(signKey[16:])
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return ""
	}
	ct := make([]byte, len(bBufPadded))
	cipher.NewCBCEncrypter(block, aesIV[:]).CryptBlocks(ct, bBufPadded)
	return base64.StdEncoding.EncodeToString(append([]byte("\xf2\x81"), ct...))
}
func majorDouyinArgus(query, stub string, timestamp int64, aid, licenseID, platform int, secDeviceID, sdkVersionStr string, sdkVersionInt int, randVal uint32) string {
	var bodyHash []byte
	if stub == "" {
		bodyHash = majorSM3(make([]byte, 16))[:6]
	} else if decoded, err := hex.DecodeString(stub); err == nil {
		bodyHash = majorSM3(decoded)[:6]
	} else {
		bodyHash = majorSM3(make([]byte, 16))[:6]
	}
	var queryHash []byte
	if query == "" {
		queryHash = majorSM3(make([]byte, 16))[:6]
	} else {
		queryHash = majorSM3([]byte(query))[:6]
	}
	values, _ := url.ParseQuery(query)
	pb := majorDouyinArgusProtobuf(aid, licenseID, platform, values.Get("device_id"), values.Get("version_name"), sdkVersionStr, sdkVersionInt, timestamp, randVal, bodyHash, queryHash, secDeviceID)
	return majorDouyinArgusEncrypt(pb)
}
func majorDouyinTraceID(deviceID string) string {
	nowMs := uint64(time.Now().UnixMilli())
	e := fmt.Sprintf("%08x", nowMs%4294967295)
	clean := strings.ReplaceAll(deviceID, "-", "")
	var e2 string
	if n, ok := new(big.Int).SetString(clean, 10); ok && n.Sign() > 0 {
		e2 = n.Text(16)
	} else {
		e2 = fmt.Sprintf("%02x", nowMs%255)
	}
	if len(e2) < 2 {
		e2 = "0" + e2
	}
	r := 22 - len(e2) - 4
	if r < 0 {
		r = 0
	}
	var randBuf [8]byte
	_, _ = rand.Read(randBuf[:])
	seed := hex.EncodeToString(randBuf[:])
	if len(seed) > r {
		seed = seed[:r]
	}
	c := fmt.Sprintf("%02d", len(e2)) + e2 + seed
	e3 := e + c
	e3Prefix := e3
	if len(e3Prefix) > 16 {
		e3Prefix = e3Prefix[:16]
	}
	return fmt.Sprintf("00-%s-%s-01", e3, e3Prefix)
}
func majorDouyinMobileSign(query string, version int) map[string]string {
	unix := time.Now().Unix()
	var randCE [2]byte
	var ladonRand [4]byte
	var argusRand [4]byte
	var secRand [9]byte
	_, _ = rand.Read(randCE[:])
	_, _ = rand.Read(ladonRand[:])
	_, _ = rand.Read(argusRand[:])
	_, _ = rand.Read(secRand[:])
	const alpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var sec strings.Builder
	sec.WriteString("AadCFwpTyztA5j9L")
	for _, b := range secRand {
		sec.WriteByte(alpha[int(b)%len(alpha)])
	}
	emptyMD5 := md5.Sum(nil)
	stub := strings.ToUpper(hex.EncodeToString(emptyMD5[:]))
	randVal := binary.LittleEndian.Uint32(argusRand[:]) & 0x7FFFFFFF
	const aid = 1128
	const licenseID = 1611921764
	headers := majorDouyinGorgon(query, "", "", unix, version, randCE)
	headers["x-ss-stub"] = stub
	headers["x-ladon"] = majorDouyinLadon(unix, licenseID, aid, ladonRand)
	headers["x-argus"] = majorDouyinArgus(query, stub, unix, aid, licenseID, 0, sec.String(), "v05.01.02-alpha.7-ov-android", 83952160, randVal)
	return headers
}
