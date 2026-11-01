// SPDX-License-Identifier: MIT
// Activation-only Go port of xhshow 0.2.0 (https://github.com/Cloxl/xhshow).
// MIT License
// Copyright (c) 2024 Cloxl
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
package parser

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math/big"
	"math/bits"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"multiparse/internal/netx"
)

const xhsActivateURI = "/api/sns/web/v1/login/activate"
const xhsSignAlphabet = "ZmserbBoHQtNP+wOcza/LpngG8yJq42KWYj0DSfdikx3VT16IlUAFM97hECvuRX5"
const xhsX3Alphabet = "MfgqrsbcyzPQRStuvC7mn501HIJBo2DEFTKdeNOwxWXYZap89+/A4UVLhijkl63G"
const xhsXorKey = "71a302257793271ddd273bcee3e4b98d9d7935e1da33f5765e2ea8afb6dc77a51a499d23b67c20660025860cbf13d4540d92497f58686c574e508f46e1956344f39139bf4faf22a3eef120b79258145b2feb5193b6478669961298e79bedca646e1a693a926154a5a7a1bd1cf0dedb742f917a747a1e388b234f2277516db7116035439730fa61e9822a0eca7bff72d8"

// Random inputs are explicit so protocol vectors do not depend on clock or RNG.
type xhsSignState struct {
	Timestamp                int64
	Seed                     uint32
	Offset, Sequence, Window uint32
	FingerprintCounter       int
	B3, Xray                 string
	XraySequence             uint32
	Direction                int
}

func xhsJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}

func xhsCustomHash(input []byte) [16]byte {
	n := uint32(len(input))
	s0, s1, s2, s3 := uint32(1831565813)^n, uint32(461845907)^(n<<8), uint32(2246822507)^(n<<16), uint32(3266489909)^(n<<24)
	for i := 0; i+8 <= len(input); i += 8 {
		v0, v1 := binary.LittleEndian.Uint32(input[i:]), binary.LittleEndian.Uint32(input[i+4:])
		s0 = bits.RotateLeft32((s0+v0)^s2, 7)
		s1 = bits.RotateLeft32((v0^s1)+s3, 11)
		s2 = bits.RotateLeft32((s2+v1)^s0, 13)
		s3 = bits.RotateLeft32((s3^v1)+s1, 17)
	}
	t0 := s0 ^ n
	t1 := s1 ^ t0
	t2 := s2 + t1
	t3 := s3 ^ t2
	r0, r1, r2, r3 := bits.RotateLeft32(t0, 9), bits.RotateLeft32(t1, 13), bits.RotateLeft32(t2, 17), bits.RotateLeft32(t3, 19)
	s0, s1 = r0+r2, r1^r3
	s2, s3 = r2+s0, r3^s1
	var out [16]byte
	for i, v := range []uint32{s0, s1, s2, s3} {
		binary.LittleEndian.PutUint32(out[i*4:], v)
	}
	return out
}

func xhsActivationXS(a1 string, body []byte, state xhsSignState) string {
	content := xhsActivateURI + string(body)
	digest, pathDigest := md5.Sum([]byte(content)), md5.Sum([]byte(xhsActivateURI))
	payload := make([]byte, 144)
	copy(payload, []byte{121, 104, 96, 41})
	binary.LittleEndian.PutUint32(payload[4:], state.Seed)
	binary.LittleEndian.PutUint64(payload[8:], uint64(state.Timestamp))
	binary.LittleEndian.PutUint64(payload[16:], uint64(state.Timestamp-int64(state.Offset)*1000))
	binary.LittleEndian.PutUint32(payload[24:], state.Sequence)
	binary.LittleEndian.PutUint32(payload[28:], state.Window)
	binary.LittleEndian.PutUint32(payload[32:], uint32(len(content)))
	for i := range 8 {
		payload[36+i] = digest[i] ^ byte(state.Seed)
	}
	payload[44] = 52
	copy(payload[45:97], a1)
	payload[97] = 10
	copy(payload[98:108], "xhs-pc-web")
	copy(payload[108:124], []byte{1, byte(state.Seed) ^ 115, 249, 65, 103, 103, 201, 181, 131, 99, 94, 7, 68, 250, 132, 21})
	copy(payload[124:], []byte{2, 97, 51, 16})
	hashInput := make([]byte, 24)
	copy(hashInput, payload[8:16])
	copy(hashInput[8:], pathDigest[:])
	hash := xhsCustomHash(hashInput)
	for i, b := range hash {
		payload[128+i] = b ^ byte(state.Seed)
	}
	key, _ := hex.DecodeString(xhsXorKey)
	for i := range payload {
		payload[i] ^= key[i]
	}
	x3 := "mns0301_" + base64.NewEncoding(xhsX3Alphabet).EncodeToString(payload)
	data := struct {
		X0 string `json:"x0"`
		X1 string `json:"x1"`
		X2 string `json:"x2"`
		X3 string `json:"x3"`
		X4 string `json:"x4"`
	}{"4.3.5", "xhs-pc-web", "Windows", x3, "object"}
	return "XYS_" + base64.NewEncoding(xhsSignAlphabet).EncodeToString(xhsJSON(data))
}

func xhsFingerprintB1(state xhsSignState) string {
	// Only these fields enter b1; unrelated browser fields in xhshow are not signed.
	fp := struct {
		X33 string `json:"x33"`
		X34 string `json:"x34"`
		X35 string `json:"x35"`
		X36 string `json:"x36"`
		X37 string `json:"x37"`
		X38 string `json:"x38"`
		X39 int    `json:"x39"`
		X42 string `json:"x42"`
		X43 string `json:"x43"`
		X44 string `json:"x44"`
		X45 string `json:"x45"`
		X46 string `json:"x46"`
		X48 string `json:"x48"`
		X49 string `json:"x49"`
		X50 string `json:"x50"`
		X51 string `json:"x51"`
		X52 string `json:"x52"`
		X82 string `json:"x82"`
	}{"0", "0", "0", strconv.Itoa(state.FingerprintCounter), "0|0|0|0|0|0|0|0|0|1|0|0|0|0|0|0|0|0|1|0|0|0|0|0", "0|0|1|0|1|0|0|0|0|0|1|0|1|0|1|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0", 0, "3.4.4", "742cc32c", strconv.FormatInt(state.Timestamp, 10), "__SEC_CAV__1-1-1-1-1|__SEC_WSA__|", "false", "", "{list:[],type:}", "", "", "", "_0x17a2|_0x1954"}
	raw := xhsJSON(fp)
	cipher, _ := rc4.NewCipher([]byte("xhswebmplfbt"))
	cipher.XORKeyStream(raw, raw)
	// xhshow decodes ARC4 bytes as latin1, then URL-quotes as UTF-8.
	// Its split('%')[1:] drops any leading safe characters before the first escape.
	encoded := make([]byte, 0, len(raw)*2)
	escaped := false
	for _, b := range raw {
		safe := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("!*'()~_.-", rune(b))
		if !escaped && safe {
			continue
		}
		escaped = true
		encoded = utf8.AppendRune(encoded, rune(b))
	}
	return base64.NewEncoding(xhsSignAlphabet).EncodeToString(encoded)
}

func xhsActivationHeaders(a1 string, body []byte, state xhsSignState) map[string]string {
	b1 := xhsFingerprintB1(state)
	common := struct {
		S0  int    `json:"s0"`
		S1  string `json:"s1"`
		X0  string `json:"x0"`
		X1  string `json:"x1"`
		X2  string `json:"x2"`
		X3  string `json:"x3"`
		X4  string `json:"x4"`
		X5  string `json:"x5"`
		X6  string `json:"x6"`
		X7  string `json:"x7"`
		X8  string `json:"x8"`
		X9  int32  `json:"x9"`
		X10 int    `json:"x10"`
		X11 string `json:"x11"`
	}{5, "", "1", "4.3.5", "Windows", "xhs-pc-web", "4.86.0", a1, "", "", b1, int32(crc32.ChecksumIEEE([]byte(b1)) ^ 0xedb88320), 0, "normal"}
	return map[string]string{"x-s": xhsActivationXS(a1, body, state), "x-s-common": base64.NewEncoding(xhsSignAlphabet).EncodeToString(xhsJSON(common)), "x-t": strconv.FormatInt(state.Timestamp, 10), "x-b3-traceid": state.B3, "x-xray-traceid": fmt.Sprintf("%016x%s", uint64(state.Timestamp)<<23|uint64(state.XraySequence), state.Xray), "x-mns": "unload", "xy-direction": strconv.Itoa(state.Direction)}
}

func xhsRandomInt(min, max int64) (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(max-min+1))
	if err != nil {
		return 0, err
	}
	return min + n.Int64(), nil
}

func majorXHSNewSession(ctx context.Context, c *http.Client) (string, error) {
	ctx = netx.WithExplicitCookies(ctx)
	now := time.Now().UnixMilli()
	random := make([]byte, 32+16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	const charset = "abcdefghijklmnopqrstuvwxyz1234567890"
	var suffix strings.Builder
	for range 30 {
		n, err := xhsRandomInt(0, 35)
		if err != nil {
			return "", err
		}
		suffix.WriteByte(charset[n])
	}
	aPart := strconv.FormatInt(now, 16) + suffix.String() + "50000"
	a1 := aPart + strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(aPart))), 10)
	if len(a1) > 52 {
		a1 = a1[:52]
	}
	webID := md5.Sum([]byte(a1))
	body := xhsJSON(struct {
		Key string `json:"client_public_key_base64"`
	}{base64.StdEncoding.EncodeToString(random[:32])})
	bounds := [][2]int64{{0, 0xffffffff}, {10, 50}, {15, 50}, {1000, 1200}, {1, 20}, {0, 8388607}, {10, 100}}
	values := make([]int64, len(bounds))
	for i, b := range bounds {
		n, err := xhsRandomInt(b[0], b[1])
		if err != nil {
			return "", err
		}
		values[i] = n
	}
	state := xhsSignState{Timestamp: now, Seed: uint32(values[0]), Offset: uint32(values[1]), Sequence: uint32(values[2]), Window: uint32(values[3]), FingerprintCounter: int(values[4]), B3: hex.EncodeToString(random[32:40]), Xray: hex.EncodeToString(random[40:48]), XraySequence: uint32(values[5]), Direction: int(values[6])}
	headers := xhsActivationHeaders(a1, body, state)
	headers["User-Agent"] = majorUA
	headers["Content-Type"] = "application/json;charset=UTF-8"
	headers["Origin"] = "https://www.xiaohongshu.com"
	headers["Referer"] = "https://www.xiaohongshu.com/"
	headers["Cookie"] = "a1=" + a1 + "; webId=" + hex.EncodeToString(webID[:]) + "; xsecappid=xhs-pc-web"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://edith.xiaohongshu.com"+xhsActivateURI, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取游客 session 失败: %s", majorSafeRequestError(err))
	}
	defer resp.Body.Close()
	// Drain only a bounded response; never expose activation bodies or cookies in errors.
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("获取游客 session 响应失败")
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "web_session" && cookie.Value != "" {
			return "web_session=" + cookie.Value, nil
		}
	}
	return "", fmt.Errorf("获取游客 session 失败: HTTP %d，未返回 web_session", resp.StatusCode)
}
