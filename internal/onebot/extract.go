package onebot

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"html"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"multiparse/internal/model"
)

const (
	maxExtractBytes = 256 << 10
	maxExtractDepth = 10
	maxExtractNodes = 4096
	maxExtractLinks = 32
	maxXMLDepth     = 32
)

var (
	webLinkPattern    = regexp.MustCompile(`(?i)https?://[^\s<>"'\\\x00-\x1f]+`)
	biliIDPattern     = regexp.MustCompile(`\b(?:(?i:BV)1[1-9A-HJ-NP-Za-km-z]{9}|(?i:av)[0-9]{1,19})(?:\?p=[0-9]{1,9})?\b`)
	htmlEntityPattern = regexp.MustCompile(`&(?:#[0-9]+|#[xX][0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]+);`)
	bareLinkPattern   = makeBareLinkPattern()
)

func makeBareLinkPattern() *regexp.Regexp {
	seen := make(map[string]bool)
	var domains []string
	for _, platform := range model.Platforms {
		for _, domain := range platform.Domains {
			if !seen[domain] {
				seen[domain] = true
				domains = append(domains, regexp.QuoteMeta(domain))
			}
		}
	}
	sort.Slice(domains, func(i, j int) bool { return len(domains[i]) > len(domains[j]) })
	return regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)*(?:` + strings.Join(domains, "|") + `)(?:/[^\s<>"'\\\x00-\x1f]*)?`)
}

type extractor struct {
	remaining int
	nodes     int
	links     []string
	seen      map[string]bool
}

// Extract returns only links belonging to model.Platforms, plus canonical Bilibili
// BV links. It decodes nested JSON, XML attributes/text and escaped share cards;
// it never resolves URLs, fetches XML entities, or follows arbitrary card targets.
func Extract(event model.Event) []string {
	x := extractor{remaining: maxExtractBytes, seen: make(map[string]bool)}
	for _, segment := range event.Segments[:min(len(event.Segments), 200)] {
		if x.exhausted() {
			break
		}
		switch segment.Type {
		case "text":
			if text, ok := segment.Data["text"].(string); ok {
				x.walkCQText(text)
			}
		case "json", "xml", "share":
			x.walk(segment.Data, 0)
		}
	}
	return x.links
}

func (x *extractor) exhausted() bool {
	return x.remaining <= 0 || x.nodes >= maxExtractNodes || len(x.links) >= maxExtractLinks
}

func (x *extractor) walkCQText(text string) {
	if len(text) > x.remaining {
		text = text[:x.remaining]
	}
	for text != "" && !x.exhausted() {
		start := strings.Index(text, "[CQ:")
		if start < 0 {
			x.walk(text, 0)
			return
		}
		x.walk(text[:start], 0)
		text = text[start:]
		end := strings.IndexByte(text, ']')
		if end < 0 {
			x.walk(text, 0)
			return
		}
		x.nodes++
		x.remaining -= min(end+1, x.remaining)
		// Split before decoding CQ's &#44;/&#93; escapes; otherwise a URL's
		// comma or closing bracket becomes a false field/card delimiter.
		fields := strings.SplitN(text[4:end], ",", 33)
		switch fields[0] {
		case "json", "xml", "share":
			for _, field := range fields[1:] {
				key, value, ok := strings.Cut(field, "=")
				if ok && (fields[0] == "share" || key == "data") {
					x.walk(unescapeCardHTML(value), 0)
				}
			}
		}
		text = text[end+1:]
	}
}

func (x *extractor) walk(value any, depth int) {
	if depth > maxExtractDepth || x.exhausted() {
		return
	}
	x.nodes++
	switch value := value.(type) {
	case string:
		x.walkString(value, depth)
	case map[string]any:
		if len(value) > maxExtractNodes-x.nodes {
			return
		}
		// Sorting keeps extraction order stable for maps decoded from share cards.
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			x.walk(value[key], depth+1)
			if x.exhausted() {
				break
			}
		}
	case []any:
		for _, item := range value {
			x.walk(item, depth+1)
			if x.exhausted() {
				break
			}
		}
	case json.RawMessage:
		x.walkString(string(value), depth)
	}
}

func (x *extractor) walkString(text string, depth int) {
	if len(text) > x.remaining {
		text = text[:x.remaining]
	}
	x.remaining -= len(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if isXMLCard(text) {
		x.walkXML(text, depth+1)
		return
	}
	text = unescapeCardHTML(text)
	if isXMLCard(text) {
		x.walkXML(text, depth+1)
		return
	}
	if depth < maxExtractDepth && !x.exhausted() && (text[0] == '{' || text[0] == '[' || text[0] == '"') {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			var trailing any
			if decoder.Decode(&trailing) == io.EOF {
				x.walk(value, depth+1)
				return
			}
		}
	}
	decoded := decodeCardEscapes(text)
	if decoded != text && depth < maxExtractDepth && !x.exhausted() {
		x.walk(decoded, depth+1)
		return
	}
	x.scan(text)
}

func unescapeCardHTML(text string) string {
	// Bare query keys such as &copy= must not become HTML entities; signed
	// links are changed only when a complete, semicolon-terminated entity exists.
	if !htmlEntityPattern.MatchString(text) {
		return text
	}
	return htmlEntityPattern.ReplaceAllStringFunc(text, html.UnescapeString)
}

func isXMLCard(text string) bool {
	if !strings.HasPrefix(text, "<") {
		return false
	}
	for _, prefix := range []string{"<http://", "<https://"} {
		if len(text) >= len(prefix) && strings.EqualFold(text[:len(prefix)], prefix) {
			return false
		}
	}
	return true
}

func (x *extractor) walkXML(text string, depth int) {
	if depth > maxExtractDepth || x.exhausted() {
		return
	}
	// encoding/xml has no external entity resolver. Leave Entity nil and never
	// interpret directives or processing instructions as addresses to retrieve.
	decoder := xml.NewDecoder(strings.NewReader(text))
	decoder.Strict = true
	xmlDepth := 0
	for !x.exhausted() {
		token, err := decoder.Token()
		if err != nil {
			return
		}
		x.nodes++
		switch token := token.(type) {
		case xml.StartElement:
			xmlDepth++
			if xmlDepth > maxXMLDepth {
				return
			}
			for _, attr := range token.Attr {
				x.walk(attr.Value, depth)
				if x.exhausted() {
					return
				}
			}
		case xml.EndElement:
			xmlDepth--
		case xml.CharData:
			x.walk(string(token), depth)
		}
	}
}

func (x *extractor) scan(text string) {
	urlSpans := webLinkPattern.FindAllStringIndex(text, maxExtractLinks*4)
	for _, span := range urlSpans {
		x.addLink(text[span[0]:span[1]])
	}
	bareSpans := bareLinkPattern.FindAllStringIndex(text, maxExtractLinks*4)
	for _, span := range bareSpans {
		if insideSpan(span[0], urlSpans) || !bareBoundary(text, span[0], span[1]) {
			continue
		}
		x.addLink("https://" + text[span[0]:span[1]])
	}
	for _, span := range biliIDPattern.FindAllStringIndex(text, maxExtractLinks*4) {
		if insideSpan(span[0], urlSpans) || insideSpan(span[0], bareSpans) {
			continue
		}
		if canonical, ok := model.BilibiliURL(text[span[0]:span[1]]); ok {
			x.addLink(canonical)
		}
	}
}

func insideSpan(position int, spans [][]int) bool {
	for _, span := range spans {
		if position >= span[0] && position < span[1] {
			return true
		}
	}
	return false
}

func bareBoundary(text string, start, end int) bool {
	if start > 0 {
		c := text[start-1]
		if c == '/' || c == '@' || c == '.' || c == '-' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			return false
		}
	}
	if end < len(text) {
		c := text[end]
		if c == '@' || c == '.' || c == '-' || c == '_' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			return false
		}
	}
	return true
}

func (x *extractor) addLink(raw string) {
	if len(x.links) >= maxExtractLinks || len(raw) > 8192 {
		return
	}
	raw = strings.TrimRight(raw, ".,;!，。；！？、】》」』")
	for len(raw) > 0 {
		last := raw[len(raw)-1]
		if last == ')' && strings.Count(raw, ")") > strings.Count(raw, "(") || last == ']' && strings.Count(raw, "]") > strings.Count(raw, "[") || last == '}' && strings.Count(raw, "}") > strings.Count(raw, "{") {
			raw = raw[:len(raw)-1]
			continue
		}
		break
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return
	}
	if port := parsed.Port(); port != "" && port != "80" && port != "443" {
		return
	}
	if _, ok := model.Identify(raw); !ok {
		return
	}
	if x.seen[raw] {
		return
	}
	x.seen[raw] = true
	x.links = append(x.links, raw)
}

// decodeCardEscapes handles escaped JSON embedded in CQ text, including repeated
// escaping. Each pass is bounded by walk's byte, node and recursion budgets.
func decodeCardEscapes(text string) string {
	if !strings.ContainsRune(text, '\\') {
		return text
	}
	var out bytes.Buffer
	out.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' || i+1 == len(text) {
			out.WriteByte(text[i])
			continue
		}
		switch text[i+1] {
		case '\\', '/', '"':
			out.WriteByte(text[i+1])
			i++
		case 'n', 'r', 't', 'b', 'f':
			out.WriteByte(' ')
			i++
		case 'u':
			if i+6 > len(text) {
				out.WriteByte(text[i])
				continue
			}
			value, err := strconv.ParseUint(text[i+2:i+6], 16, 16)
			if err != nil {
				out.WriteByte(text[i])
				continue
			}
			r := rune(value)
			i += 5
			if utf16.IsSurrogate(r) && i+6 < len(text) && text[i+1:i+3] == `\u` {
				second, err := strconv.ParseUint(text[i+3:i+7], 16, 16)
				if err == nil {
					r = utf16.DecodeRune(r, rune(second))
					i += 6
				}
			}
			if !utf8.ValidRune(r) {
				r = utf8.RuneError
			}
			out.WriteRune(r)
		default:
			out.WriteByte(text[i])
		}
	}
	return out.String()
}
