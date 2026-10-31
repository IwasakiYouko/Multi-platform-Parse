// SPDX-License-Identifier: MIT
// Adapted from ParseHub, Copyright (c) 2024 梓澪; full MIT notice in major_common.go.
package parser

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"math/bits"
	"net/url"
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
func majorDouyinSignedQuery(params url.Values, cookie string) (string, map[string]string, error) {
	query := params.Encode()
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, err
	}
	start := time.Now().UnixMilli()
	params.Set("a_bogus", majorDouyinABogus(query, start, start+6, random))
	if fp := majorCookie(cookie, "s_v_web_id"); fp != "" {
		params.Set("verifyFp", fp)
		params.Set("fp", fp)
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
		params.Set("uifid", uifid)
		params.Set("timestamp", timestamp)
		query = strings.ReplaceAll(params.Encode(), "+", "%20")
		sum := md5.Sum([]byte(uifid + "_" + timestamp + "_A96D855A08C0A9707F8BEF0D9A527E4E_" + query))
		signature := hex.EncodeToString(sum[:])
		h["uifid"] = uifid
		h["x-secsdk-web-signature"] = signature
		h["x-secsdk-web-expire"] = timestamp
		return query + "&x-secsdk-web-signature=" + signature, h, nil
	}
	return params.Encode(), h, nil
}
