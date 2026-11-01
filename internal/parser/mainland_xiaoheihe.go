package parser

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() { Register("xiaoheihe", mainlandXiaoheihe) }
func mainlandXiaoheihe(ctx context.Context, c *http.Client, raw, _ string) (*model.ParseResult, error) {
	ctx = netx.WithExplicitCookies(ctx)
	u, e := url.Parse(raw)
	if e != nil {
		return nil, e
	}
	if !mainlandXHHHost(u) {
		return nil, fmt.Errorf("小黑盒链接必须使用官方域名")
	}
	id := mainlandXHHLinkID(u)
	if id == "" {
		_, final, err := Get(ctx, c, raw, Headers("", raw))
		if err != nil {
			return nil, err
		}
		u, e = url.Parse(final)
		if e != nil || !mainlandXHHHost(u) {
			return nil, fmt.Errorf("小黑盒跳转到非官方地址")
		}
		id = mainlandXHHLinkID(u)
	}
	if id == "" {
		return nil, fmt.Errorf("小黑盒链接缺少 link_id")
	}
	token, e := mainlandXHHDevice(ctx, c)
	if e != nil {
		return nil, fmt.Errorf("小黑盒设备认证失败: %w", e)
	}
	now := time.Now().Unix()
	var random [8]byte
	if _, e = rand.Read(random[:]); e != nil {
		return nil, e
	}
	nonce := mainlandXHHNonce(now, strconv.FormatFloat(float64(binary.BigEndian.Uint64(random[:])>>11)/(1<<53), 'f', -1, 64))
	q := url.Values{"os_type": {"web"}, "app": {"heybox"}, "client_type": {"web"}, "version": {"999.0.4"}, "web_version": {"2.5"}, "x_client_type": {"web"}, "x_app": {"heybox_website"}, "heybox_id": {""}, "x_os_type": {"Windows"}, "device_info": {"Chrome"}, "link_id": {id}, "owner_only": {"1"}, "_time": {strconv.FormatInt(now, 10)}, "nonce": {nonce}, "hkey": {mainlandXHHSign("/bbs/app/link/tree", now+1, nonce)}}
	var v any
	if e = JSON(ctx, c, "https://api.xiaoheihe.cn/bbs/app/link/tree?"+q.Encode(), Headers("x_xhh_tokenid="+token, "https://www.xiaoheihe.cn/"), &v); e != nil {
		return nil, e
	}
	if status := Text(Path(v, "status")); status != "ok" {
		messages := map[string]string{"login": "需要登录", "show_captcha": "需要过验证码", "relogin": "需要重新登录", "failed": "请求失败", "lack_token": "缺少token"}
		message := messages[status]
		if message == "" {
			message = status
		}
		return nil, fmt.Errorf("小黑盒接口 %s: %s", message, Text(Path(v, "msg")))
	}
	link := Obj(Path(v, "result", "link"))
	if len(link) == 0 {
		return nil, fmt.Errorf("小黑盒未返回帖子正文")
	}
	r := mainlandResult("xiaoheihe", raw)
	r.Title = Text(link["title"])
	r.Author = mainlandFirst(Path(link, "user", "username"), Path(link, "user", "nickname"))
	if mainlandXHHTruthy(link["has_video"]) {
		r.Type = "video"
		r.Content = Text(link["text"])
		mainlandAdd(r, "video", Text(link["video_url"]), map[string]any{"thumbnail": link["video_thumb"]})
		return mainlandFinish(r, true)
	}
	var parts []any
	if e = json.Unmarshal([]byte(Text(link["text"])), &parts); e != nil {
		return nil, fmt.Errorf("小黑盒正文格式错误: %w", e)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("小黑盒正文内容为空")
	}
	first := Text(Path(parts[0], "text"))
	concept := mainlandXHHTruthy(link["use_concept_type"])
	if Text(Path(parts[0], "type")) == "html" {
		doc, err := mainlandDoc(first)
		if err != nil {
			return nil, err
		}
		doc.Find("img").Each(func(_ int, s *goquery.Selection) {
			original, _ := s.Attr("data-original")
			s.SetAttr("src", mainlandXHHImage(original))
			s.RemoveAttr("data-original").RemoveAttr("data-src")
		})
		first, e = doc.Html()
		if e != nil {
			return nil, e
		}
		referenceSetHTML(r, first)
		if concept {
			r.Content = r.MarkdownContent
			r.MarkdownContent = ""
		}
	} else {
		r.Content = first
		if !concept {
			r.MarkdownContent = first
		}
	}
	r.Type = "article"
	if concept {
		r.Type = "image"
	}
	for _, p := range parts[1:] {
		if Text(Path(p, "type")) != "img" {
			continue
		}
		thumbnail := Text(Path(p, "url"))
		kind := "image"
		if strings.Contains(thumbnail, ".gif") {
			kind = "gif"
			if concept {
				r.Type = "multimedia"
			}
		}
		mainlandAdd(r, kind, mainlandXHHImage(thumbnail), map[string]any{"thumbnail": thumbnail, "width": Path(p, "width"), "height": Path(p, "height")})
	}
	if r.Content == "" && len(r.Media) == 0 && r.Title != "" {
		return r, nil
	}
	return mainlandFinish(r, false)
}

func mainlandXHHHost(u *url.URL) bool {
	return u != nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.Port() == "" && (u.Hostname() == "xiaoheihe.cn" || strings.HasSuffix(u.Hostname(), ".xiaoheihe.cn"))
}

func mainlandXHHLinkID(u *url.URL) string {
	if id := u.Query().Get("link_id"); id != "" {
		return id
	}
	if m := regexp.MustCompile(`/link/([^/?]+)`).FindStringSubmatch(u.Path); len(m) > 0 {
		return m[1]
	}
	return ""
}

func mainlandXHHTruthy(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case []any:
		return len(value) != 0
	case map[string]any:
		return len(value) != 0
	default:
		return Number(value) != 0
	}
}

func mainlandXHHNonce(now int64, randomDecimal string) string {
	return strings.ToUpper(mainlandMD5(strconv.FormatInt(now, 10) + randomDecimal))
}
func mainlandXHHImage(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return raw
	}
	u.RawQuery = ""
	u.Path = regexp.MustCompile(`/thumb\.(\w+)$`).ReplaceAllString(u.Path, ".$1")
	return u.String()
}
func mainlandMD5(s string) string { v := md5.Sum([]byte(s)); return hex.EncodeToString(v[:]) }
func mainlandUUID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func mainlandXHHSign(path string, t int64, nonce string) string {
	const table = "AB45STUVWZEFGJ6CH01D237IXYPQRKLMN89"
	mapping := func(s string, n int) string {
		var b strings.Builder
		for _, c := range s {
			b.WriteByte(table[int(c)%n])
		}
		return b.String()
	}
	mapped := []string{mapping(strconv.FormatInt(t, 10), len(table)-2), mapping("/"+strings.Join(strings.FieldsFunc(path, func(r rune) bool { return r == '/' }), "/")+"/", len(table)), mapping(nonce, len(table))}
	var b strings.Builder
	for i := range max(len(mapped[0]), len(mapped[1]), len(mapped[2])) {
		if b.Len() == 20 {
			break
		}
		for _, s := range mapped {
			if i < len(s) {
				b.WriteByte(s[i])
				if b.Len() == 20 {
					break
				}
			}
		}
	}
	sum := mainlandMD5(b.String())
	tail := []byte(sum[len(sum)-6:])
	xt := func(x byte) byte { return x<<1 ^ byte(0x1b*int(x>>7)) }
	mul3 := func(x byte) byte { return xt(x) ^ x }
	mul6 := func(x byte) byte { return mul3(xt(x)) }
	mul12 := func(x byte) byte { return mul6(mul3(xt(x))) }
	mul14 := func(x byte) byte { return mul12(x) ^ mul6(x) ^ mul3(x) }
	mixed := []byte{mul14(tail[0]) ^ mul12(tail[1]) ^ mul6(tail[2]) ^ mul3(tail[3]), mul3(tail[0]) ^ mul14(tail[1]) ^ mul12(tail[2]) ^ mul6(tail[3]), mul6(tail[0]) ^ mul3(tail[1]) ^ mul14(tail[2]) ^ mul12(tail[3]), mul12(tail[0]) ^ mul6(tail[1]) ^ mul3(tail[2]) ^ mul14(tail[3]), tail[4], tail[5]}
	n := 0
	for _, v := range mixed {
		n += int(v)
	}
	return mapping(sum[:5], len(table)-4) + fmt.Sprintf("%02d", n%100)
}

// Device-profile protocol follows ParseHub's SecuritySm adaptation of
// https://github.com/YueHen14/skyland-auto-sign/blob/6e7115b5580377c842f50f05b0fa39ab079c17b1/SecuritySm.py.
func mainlandXHHDevice(ctx context.Context, c *http.Client) (string, error) {
	ctx = netx.WithoutCookies(ctx)
	const publicKey = "MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCXj9exmI4nQjmT52iwr+yf7hAQ06bfSZHTAHUfRBYiagCf/whhd8es0R79wBigpiHLd28TKA8b8mGR8OiiI1hV+qfynCWihvp3mdj8MiiH6SU3lhro2hkfYzImZB0RmWr2zE4Xt1+A6Oyp6bf+W7JSxYUXHw3nNv7Td4jw4jEFKQIDAQAB"
	keyBytes, e := base64.StdEncoding.DecodeString(publicKey)
	if e != nil {
		return "", e
	}
	pk, e := x509.ParsePKIXPublicKey(keyBytes)
	if e != nil {
		return "", e
	}
	rsaKey, ok := pk.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("设备公钥格式错误")
	}
	uid, e := mainlandUUID()
	if e != nil {
		return "", e
	}
	encrypted, e := rsa.EncryptPKCS1v15(rand.Reader, rsaKey, []byte(uid))
	if e != nil {
		return "", e
	}
	priID := []byte(mainlandMD5(uid)[:16])
	vpw, e := mainlandUUID()
	if e != nil {
		return "", e
	}
	trees, e := mainlandUUID()
	if e != nil {
		return "", e
	}
	smuid, e := mainlandUUID()
	if e != nil {
		return "", e
	}
	smid := time.Now().Format("20060102150405") + mainlandMD5(smuid) + "00"
	smid += mainlandMD5("smsk_web_" + smid)[:14] + "0"
	now := time.Now().UnixMilli()
	env := map[string]any{"plugins": "MicrosoftEdgePDFPluginPortableDocumentFormatinternal-pdf-viewer1,MicrosoftEdgePDFViewermhjfbmdgcfjbbpaeojofohoefgiehjai1", "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.0.0", "canvas": "259ffe69", "timezone": int64(-480), "platform": "Win32", "url": "https://www.skland.com/", "referer": "", "res": "1920_1080_24_1.25", "clientSize": "0_0_1080_1920_1920_1080_1920_1080", "status": "0011", "vpw": vpw, "svm": now, "trees": trees, "pmf": now, "protocol": int64(102), "organization": "0yD85BjYvGFAvHaSQ1mc", "appId": "heybox_website", "os": "web", "version": "3.0.0", "sdkver": "3.0.0", "box": "", "rtype": "all", "smid": smid, "subVersion": "1.0.0", "time": int64(0)}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var tn strings.Builder
	for _, k := range keys {
		switch v := env[k].(type) {
		case int64:
			tn.WriteString(strconv.FormatInt(v*10000, 10))
		case string:
			tn.WriteString(v)
		}
	}
	env["tn"] = mainlandMD5(tn.String())
	rules := map[string][2]string{"appId": {"xx", "uy7mzc4h"}, "box": {"jf", ""}, "canvas": {"yk", "snrn887t"}, "clientSize": {"zx", "cpmjjgsu"}, "organization": {"dp", "78moqjfc"}, "os": {"pj", "je6vk6t4"}, "platform": {"gm", "pakxhcd2"}, "plugins": {"kq", "v51m3pzl"}, "pmf": {"vw", "2mdeslu3"}, "protocol": {"protocol", ""}, "referer": {"ab", "y7bmrjlc"}, "res": {"hf", "whxqm2a7"}, "rtype": {"lo", "x8o2h2bl"}, "sdkver": {"sc", "9q3dcxp2"}, "status": {"an", "2jbrxxw4"}, "subVersion": {"ns", "eo3i2puh"}, "svm": {"qr", "fzj3kaeh"}, "time": {"nb", "q2t3odsk"}, "timezone": {"as", "1uv05lj5"}, "tn": {"py", "x9nzj1bp"}, "trees": {"pi", "acfs0xo4"}, "ua": {"bj", "k92crp1t"}, "url": {"cf", "y95hjkoo"}, "version": {"version", ""}, "vpw": {"ca", "r9924ab5"}}
	obfuscated := map[string]any{}
	for k, v := range env {
		rule, ok := rules[k]
		if !ok {
			obfuscated[k] = v
			continue
		}
		if rule[1] == "" {
			obfuscated[rule[0]] = v
			continue
		}
		value, err := mainlandXHHDES(Text(v), rule[1])
		if err != nil {
			return "", err
		}
		obfuscated[rule[0]] = value
	}
	b, e := json.Marshal(obfuscated)
	if e != nil {
		return "", e
	}
	var compressed bytes.Buffer
	gz, e := gzip.NewWriterLevel(&compressed, 2)
	if e != nil {
		return "", e
	}
	if _, e = gz.Write(b); e != nil {
		return "", e
	}
	if e = gz.Close(); e != nil {
		return "", e
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(compressed.Bytes()))
	encoded = append(encoded, 0)
	if rem := len(encoded) % 16; rem != 0 {
		encoded = append(encoded, make([]byte, 16-rem)...)
	}
	block, e := aes.NewCipher(priID)
	if e != nil {
		return "", e
	}
	cipher.NewCBCEncrypter(block, []byte("0102030405060708")).CryptBlocks(encoded, encoded)
	var response any
	if e = mainlandPostJSON(ctx, c, "https://fp-it.portal101.cn/deviceprofile/v4", Headers("", "https://www.xiaoheihe.cn/"), map[string]any{"appId": "heybox_website", "compress": 2, "data": hex.EncodeToString(encoded), "encode": 5, "ep": base64.StdEncoding.EncodeToString(encrypted), "organization": "0yD85BjYvGFAvHaSQ1mc", "os": "web"}, &response); e != nil {
		return "", e
	}
	if Number(Path(response, "code")) != 1100 || Text(Path(response, "detail", "deviceId")) == "" {
		return "", fmt.Errorf("数美设备认证拒绝请求: %s", mainlandFirst(Path(response, "message"), Path(response, "msg"), Path(response, "code")))
	}
	return "B" + Text(Path(response, "detail", "deviceId")), nil
}

func mainlandXHHDES(value, key string) (string, error) {
	block, err := des.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}
	// SecuritySm's update-only DES emits complete blocks after eight zero bytes.
	plain := make([]byte, (len(value)+8)/8*8)
	copy(plain, value)
	for i := 0; i < len(plain); i += 8 {
		block.Encrypt(plain[i:i+8], plain[i:i+8])
	}
	return base64.StdEncoding.EncodeToString(plain), nil
}
