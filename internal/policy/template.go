package policy

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"multiparse/internal/model"
)

var globalTrackingParams = map[string]bool{
	"utm_source":       true,
	"utm_medium":       true,
	"utm_campaign":     true,
	"utm_term":         true,
	"utm_content":      true,
	"utm_id":           true,
	"fbclid":           true,
	"gclid":            true,
	"msclkid":          true,
	"igshid":           true,
	"igsh":             true,
	"vd_source":        true,
	"spm_id_from":      true,
	"from_spmid":       true,
	"from_source":      true,
	"seid":             true,
	"share_source":     true,
	"share_medium":     true,
	"share_plat":       true,
	"share_session_id": true,
	"share_from":       true,
	"share_tag":        true,
	"share_copy_link":  true,
	"unique_k":         true,
	"bbid":             true,
	"buvid":            true,
	"is_story_h5":      true,
	"up_id":            true,
	"plat_id":          true,
	"tt_from":          true,
	"u_code":           true,
	"iid":              true,
	"did":              true,
	"enter_from":       true,
	"enter_method":     true,
	"is_from_webapp":   true,
	"sender_device":    true,
	"web_id":           true,
	"_r":               true,
	"_t":               true,
	"share_app_id":     true,
	"share_link_id":    true,
	"share_item_id":    true,
	"ug_btm":           true,
	"ugbiz_name":       true,
	"xhsshare":         true,
	"apptime":          true,
	"appuid":           true,
	"shareredid":       true,
}

var (
	douyinVideoPathRe = regexp.MustCompile(`^/(?:share/)?video/([0-9]+)(?:/|$)`)
	douyinNotePathRe  = regexp.MustCompile(`^/(?:share/)?(?:note|slides)/([0-9]+)(?:/|$)`)
	ksPhotoPathRe     = regexp.MustCompile(`^/(?:short-video|fw/photo)/([^/?#]+)(?:/|$)`)
	digitsOnlyRe      = regexp.MustCompile(`^[0-9]+$`)

	xhsTopicTagRe    = regexp.MustCompile(`#([^#\[\]\r\n]{1,40})\[话题\]#`)
	doubleHashTagRe  = regexp.MustCompile(`#([^#\s\r\n][^#\r\n]{0,38}?)#`)
	singleHashTagRe  = regexp.MustCompile(`(?:^|[\s])#([\p{L}\p{N}_][\p{L}\p{N}_\-]{0,39})`)
	trailingTagsRe   = regexp.MustCompile(`(?:^|\s+)#[\p{L}\p{N}_][\p{L}\p{N}_\-]{0,39}#?(?:\s*#[\p{L}\p{N}_][\p{L}\p{N}_\-]{0,39}#?)*\s*$`)
	pureTagLineToken = regexp.MustCompile(`^#[\p{L}\p{N}_][\p{L}\p{N}_\-]{0,39}#?$`)
)

func allowQueryKeys(q url.Values, allowed ...string) string {
	if len(q) == 0 || len(allowed) == 0 {
		return ""
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		allowSet[strings.ToLower(k)] = true
	}
	kept := url.Values{}
	for k, vals := range q {
		lk := strings.ToLower(k)
		if !allowSet[lk] {
			continue
		}
		if lk == "p" || lk == "pn" {
			if len(vals) == 1 && (vals[0] == "" || vals[0] == "1") {
				continue
			}
		}
		kept[k] = vals
	}
	return kept.Encode()
}

func matchDomain(host string, domains ...string) bool {
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// CleanTrackingURL canonicalizes known platform URLs using strict per-platform
// parameter allowlists and strips analytics/tracking query parameters everywhere else.
func CleanTrackingURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return trimmed
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	q, qErr := url.ParseQuery(u.RawQuery)
	if qErr != nil {
		q = url.Values{}
	}

	switch {
	case matchDomain(host, "b23.tv", "bili2233.cn"):
		u.Fragment = ""
		u.RawQuery = allowQueryKeys(q, "p")
		return u.String()

	case matchDomain(host, "bilibili.com"):
		u.Fragment = ""
		if host == "m.bilibili.com" || host == "bilibili.com" {
			u.Host = "www.bilibili.com"
		}
		if strings.HasPrefix(u.Path, "/video/") {
			u.Path = strings.TrimRight(u.Path, "/")
			u.RawQuery = allowQueryKeys(q, "p", "t")
		} else {
			u.RawQuery = ""
		}
		return u.String()

	case matchDomain(host, "douyin.com", "iesdouyin.com"):
		u.Fragment = ""
		if host == "v.douyin.com" {
			u.RawQuery = ""
			return u.String()
		}
		if m := douyinVideoPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://www.douyin.com/video/" + m[1]
		}
		if m := douyinNotePathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://www.douyin.com/note/" + m[1]
		}
		if modal := q.Get("modal_id"); digitsOnlyRe.MatchString(modal) {
			return "https://www.douyin.com/video/" + modal
		}
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "xhslink.com", "xhslink.cn"):
		u.Fragment = ""
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "xiaohongshu.com"):
		u.Fragment = ""
		u.Scheme = "https"
		u.Host = "www.xiaohongshu.com"
		if strings.HasPrefix(u.Path, "/discovery/item/") {
			id := strings.Trim(strings.TrimPrefix(u.Path, "/discovery/item/"), "/")
			if id != "" && !strings.Contains(id, "/") {
				u.Path = "/explore/" + id
			}
		}
		// Keep only xsec_token / xsec_source so web browsers do not hit XHS's 404 gate,
		// stripping appuid, apptime, xhsshare, shareRedId, author_share, etc.
		u.RawQuery = allowQueryKeys(q, "xsec_token", "xsec_source")
		return u.String()

	case matchDomain(host, "kuaishou.com", "chenzhongtech.com", "gifshow.com"):
		u.Fragment = ""
		if m := ksPhotoPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://www.kuaishou.com/short-video/" + strings.TrimSuffix(m[1], ".html")
		}
		u.RawQuery = allowQueryKeys(q, "photoId")
		return u.String()

	case matchDomain(host, "weibo.com", "weibo.cn"):
		u.Fragment = ""
		if strings.HasPrefix(u.Path, "/show") {
			u.RawQuery = allowQueryKeys(q, "fid")
		} else {
			u.RawQuery = ""
		}
		return u.String()

	case matchDomain(host, "tieba.baidu.com", "tiebap.baidu.com"):
		u.Fragment = ""
		u.Host = "tieba.baidu.com"
		u.RawQuery = allowQueryKeys(q, "see_lz", "pn")
		return u.String()

	case matchDomain(host, "mp.weixin.qq.com"):
		u.Fragment = ""
		if strings.HasPrefix(u.Path, "/s/") && len(u.Path) > 3 {
			u.RawQuery = ""
		} else {
			u.RawQuery = allowQueryKeys(q, "__biz", "mid", "idx", "sn")
		}
		return u.String()

	case matchDomain(host, "twitter.com", "x.com", "fixupx.com"):
		u.Fragment = ""
		u.Scheme = "https"
		u.Host = "x.com"
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "t.co", "instagram.com", "instagr.am", "threads.net", "threads.com", "tiktok.com", "zhihu.com", "zhihu.link", "coolapk.com", "coolapk1s.com", "pipix.com", "toutiao.com", "bgm.tv", "bangumi.tv", "chii.in", "medium.com", "dou.bz", "douc.cc"):
		u.Fragment = ""
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "douban.com"):
		u.Fragment = ""
		u.RawQuery = allowQueryKeys(q, "uri")
		return u.String()

	case matchDomain(host, "xiaoheihe.cn", "xiaoheihe.com"):
		u.Fragment = ""
		u.RawQuery = allowQueryKeys(q, "link_id")
		return u.String()

	case matchDomain(host, "izuiyou.com", "xiaochuankeji.cn"):
		u.Fragment = ""
		u.RawQuery = allowQueryKeys(q, "pid")
		return u.String()

	case matchDomain(host, "youtube.com", "youtu.be"):
		u.Fragment = ""
		if host == "youtu.be" {
			u.RawQuery = allowQueryKeys(q, "t")
		} else if strings.HasPrefix(u.Path, "/watch") {
			u.RawQuery = allowQueryKeys(q, "v", "t", "list", "index")
		} else {
			u.RawQuery = allowQueryKeys(q, "t")
		}
		return u.String()

	case matchDomain(host, "facebook.com", "fb.watch", "fb.com", "snapchat.com"):
		u.Fragment = ""
		u.RawQuery = allowQueryKeys(q, "v", "id", "story_fbid", "fbid")
		return u.String()
	}

	if u.RawQuery == "" {
		return trimmed
	}
	changed := false
	for key := range q {
		lk := strings.ToLower(key)
		if globalTrackingParams[lk] || strings.HasPrefix(lk, "utm_") || strings.HasPrefix(lk, "share_") || strings.HasPrefix(lk, "spm_") {
			delete(q, key)
			changed = true
		}
	}
	if !changed {
		return trimmed
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// WorkIdentityURL returns a token-neutral canonical identity URL for deduplicating
// cooldowns and cached works across distinct share links (e.g., XHS links with per-share xsec_token).
func WorkIdentityURL(raw string) string {
	cleaned := CleanTrackingURL(raw)
	if cleaned == "" {
		return ""
	}
	u, err := url.Parse(cleaned)
	if err != nil || u.Host == "" {
		return cleaned
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if matchDomain(host, "xiaohongshu.com") && u.RawQuery != "" {
		q, qErr := url.ParseQuery(u.RawQuery)
		if qErr == nil {
			q.Del("xsec_token")
			q.Del("xsec_source")
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return cleaned
}

// EffectiveSourceURL prefers the resolved CanonicalURL over the raw input SourceURL.
func EffectiveSourceURL(r model.ParseResult) string {
	if s := strings.TrimSpace(r.CanonicalURL); s != "" {
		return s
	}
	return strings.TrimSpace(r.SourceURL)
}

// DisplayURL formats a raw URL according to global CleanURL policy.
func DisplayURL(cfg model.Config, raw string) string {
	if cfg.Media.CleanURL {
		return CleanTrackingURL(raw)
	}
	return strings.TrimSpace(raw)
}

// DisplayResultURL formats the best URL of a ParseResult according to global CleanURL policy.
func DisplayResultURL(cfg model.Config, r model.ParseResult) string {
	if cfg.Media.CleanURL {
		return CleanTrackingURL(EffectiveSourceURL(r))
	}
	if strings.TrimSpace(r.SourceURL) != "" {
		return strings.TrimSpace(r.SourceURL)
	}
	return strings.TrimSpace(r.CanonicalURL)
}

// EffectiveShowCover resolves per-platform cover display policy with fallback to global Media.ShowCover.
func EffectiveShowCover(cfg model.Config, platform string) bool {
	if pol, ok := cfg.Platforms[platform]; ok {
		switch pol.CoverMode {
		case "on":
			return true
		case "off":
			return false
		}
	}
	return cfg.Media.ShowCover
}

func cleanTagToken(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "[话题]")
	s = strings.Trim(s, "# \t\r\n")
	s = strings.TrimSuffix(s, "[话题]")
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > 40 || digitsOnlyRe.MatchString(s) {
		return ""
	}
	return s
}

func isPureHashtagLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "##") {
		return false
	}
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return unicode.IsSpace(r) || r == '、' || r == '，' || r == ','
	})
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if !pureTagLineToken.MatchString(f) || cleanTagToken(f) == "" {
			return false
		}
	}
	return true
}

// ExtractAndCleanTags merges explicit platform tags with hashtags discovered in text,
// and removes redundant trailing/standalone hashtag walls from text.
func ExtractAndCleanTags(explicit []string, text string) ([]string, string) {
	seen := make(map[string]bool)
	var tags []string
	add := func(candidate string) {
		t := cleanTagToken(candidate)
		if t == "" {
			return
		}
		key := strings.ToLower(t)
		if !seen[key] {
			seen[key] = true
			tags = append(tags, t)
		}
	}
	for _, item := range explicit {
		add(item)
	}
	if text == "" {
		return tags, ""
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	for _, m := range xhsTopicTagRe.FindAllStringSubmatch(normalized, -1) {
		add(m[1])
	}
	normalized = xhsTopicTagRe.ReplaceAllString(normalized, "#$1")

	for _, m := range doubleHashTagRe.FindAllStringSubmatch(normalized, -1) {
		add(m[1])
	}
	for _, m := range singleHashTagRe.FindAllStringSubmatch(normalized, -1) {
		add(m[1])
	}

	// Strip trailing pure-hashtag lines and trailing hashtag clusters so {tags} and {content} never duplicate.
	lines := strings.Split(normalized, "\n")
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last == "" {
			lines = lines[:len(lines)-1]
			continue
		}
		if isPureHashtagLine(last) {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}
	if len(lines) > 0 {
		lastIdx := len(lines) - 1
		line := lines[lastIdx]
		if !strings.HasPrefix(strings.TrimSpace(line), "# ") && !strings.HasPrefix(strings.TrimSpace(line), "##") {
			stripped := trailingTagsRe.ReplaceAllString(line, "")
			lines[lastIdx] = strings.TrimRight(stripped, " \t")
			if strings.TrimSpace(lines[lastIdx]) == "" {
				lines = lines[:lastIdx]
			}
		}
	}
	cleaned := strings.TrimSpace(strings.Join(lines, "\n"))
	return tags, cleaned
}

func formatTags(tags []string, maxTags int) (styled string, raw string) {
	if len(tags) == 0 {
		return "", ""
	}
	limit := len(tags)
	if maxTags > 0 && limit > maxTags {
		limit = maxTags
	}
	selected := tags[:limit]
	hashed := make([]string, len(selected))
	for i, t := range selected {
		hashed[i] = "#" + t
	}
	return strings.Join(hashed, " "), strings.Join(selected, " · ")
}

func typeLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "video":
		return "视频"
	case "image":
		return "图集"
	case "multimedia":
		return "混合媒体"
	case "article":
		return "文章"
	case "audio":
		return "音频"
	case "livephoto":
		return "实况图"
	case "dynamic":
		return "动态"
	default:
		return strings.TrimSpace(raw)
	}
}

func formatMediaStats(r model.ParseResult, files []model.Artifact) string {
	var videos, images, livephotos, audios, others int
	if len(files) > 0 {
		for _, f := range files {
			switch f.Kind {
			case "cover":
				continue
			case "video":
				videos++
			case "image", "gif":
				images++
			case "livephoto":
				livephotos++
			case "audio":
				audios++
			default:
				others++
			}
		}
	} else {
		for _, m := range r.Media {
			switch m.Kind {
			case "video":
				videos++
			case "image", "gif":
				images++
			case "livephoto":
				livephotos++
			case "audio":
				audios++
			default:
				others++
			}
		}
	}
	var parts []string
	if videos > 0 {
		parts = append(parts, fmt.Sprintf("%d 个视频", videos))
	}
	if images > 0 {
		parts = append(parts, fmt.Sprintf("%d 张图片", images))
	}
	if livephotos > 0 {
		parts = append(parts, fmt.Sprintf("%d 张实况图", livephotos))
	}
	if audios > 0 {
		parts = append(parts, fmt.Sprintf("%d 段音频", audios))
	}
	if others > 0 {
		parts = append(parts, fmt.Sprintf("%d 个文件", others))
	}
	if len(parts) == 0 {
		return ""
	}
	return "共 " + strings.Join(parts, " · ")
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

var templateTokenOrder = []string{
	"{raw_clean_url}",
	"{raw_platform}",
	"{raw_content}",
	"{raw_author}",
	"{raw_title}",
	"{raw_tags}",
	"{raw_type}",
	"{raw_url}",
	"{media_stats}",
	"{clean_url}",
	"{platform}",
	"{content}",
	"{author}",
	"{title}",
	"{tags}",
	"{type}",
	"{url}",
}

// ResolveMessageTemplate returns the effective template string for a platform,
// or "" when text info output is disabled.
func ResolveMessageTemplate(cfg model.Config, platform string) string {
	if pol, ok := cfg.Platforms[platform]; ok {
		switch pol.TemplateMode {
		case "none":
			return ""
		case "custom":
			return pol.MessageTemplate
		}
	}
	if !cfg.Media.ShowInfo {
		return ""
	}
	if strings.TrimSpace(cfg.Media.MessageTemplate) == "" {
		return model.DefaultMessageTemplate
	}
	return cfg.Media.MessageTemplate
}

func prepareCleanedBody(r model.ParseResult, stripTags bool) ([]string, string, string) {
	_, cleanedTitle := ExtractAndCleanTags(nil, r.Title)
	if cleanedTitle == "" {
		cleanedTitle = strings.TrimSpace(r.Title)
	}

	contentSource := r.Content
	isMarkdown := false
	if r.MarkdownContent != "" {
		contentSource = r.MarkdownContent
		isMarkdown = true
	}
	contentSource = strings.TrimRight(strings.ReplaceAll(contentSource, "\r\n", "\n"), "\n")

	var tags []string
	cleanedContent := contentSource
	if isMarkdown {
		tags, _ = ExtractAndCleanTags(r.Tags, r.Content)
	} else if stripTags {
		tags, cleanedContent = ExtractAndCleanTags(r.Tags, contentSource)
	} else {
		tags, _ = ExtractAndCleanTags(r.Tags, contentSource)
	}
	return tags, cleanedTitle, cleanedContent
}

// FullTextForForward returns the cleaned content (up to 8,000 runes) and whether Message 1 truncated it.
func FullTextForForward(cfg model.Config, r model.ParseResult) (string, bool) {
	tmpl := ResolveMessageTemplate(cfg, r.Platform)
	if !strings.Contains(tmpl, "{content}") && !strings.Contains(tmpl, "{raw_content}") {
		return "", false
	}
	hasTagsVar := strings.Contains(tmpl, "{tags}") || strings.Contains(tmpl, "{raw_tags}")
	_, cleanedTitle, cleanedContent := prepareCleanedBody(r, hasTagsVar)
	if strings.TrimSpace(cleanedContent) == "" {
		return "", false
	}
	hasTitleVar := strings.Contains(tmpl, "{title}") || strings.Contains(tmpl, "{raw_title}")
	if hasTitleVar && (strings.TrimSpace(cleanedContent) == strings.TrimSpace(r.Title) || strings.TrimSpace(cleanedContent) == strings.TrimSpace(cleanedTitle)) {
		return "", false
	}
	limit := cfg.Media.SummaryLength
	if limit <= 0 || limit > 8000 {
		limit = 8000
	}
	runes := []rune(cleanedContent)
	if len(runes) <= limit {
		return "", false
	}
	return truncateRunes(cleanedContent, 8000), true
}

// FormatMessage renders a ParseResult using the effective global or per-platform template.
// Lines containing template variables where all referenced variables expand to empty strings
// are automatically omitted without affecting static user text or intentional blank lines.
func FormatMessage(cfg model.Config, r model.ParseResult, files []model.Artifact) string {
	tmpl := ResolveMessageTemplate(cfg, r.Platform)
	return FormatWithTemplate(cfg, tmpl, r, files)
}

// FormatWithTemplate renders a ParseResult with an explicit template string.
func FormatWithTemplate(cfg model.Config, tmpl string, r model.ParseResult, files []model.Artifact) string {
	if strings.TrimSpace(tmpl) == "" {
		return ""
	}
	tmpl = strings.ReplaceAll(tmpl, "\r\n", "\n")

	hasTitleVar := strings.Contains(tmpl, "{title}") || strings.Contains(tmpl, "{raw_title}")
	hasTagsVar := strings.Contains(tmpl, "{tags}") || strings.Contains(tmpl, "{raw_tags}")
	hasContentVar := strings.Contains(tmpl, "{content}") || strings.Contains(tmpl, "{raw_content}")

	tags, cleanedTitle, contentSource := prepareCleanedBody(r, hasTagsVar)

	titleText := strings.TrimSpace(r.Title)
	if hasTagsVar && cleanedTitle != "" && strings.TrimSpace(r.Content) == strings.TrimSpace(r.Title) {
		titleText = cleanedTitle
	}
	rawTitle := truncateRunes(titleText, 300)
	rawAuthor := truncateRunes(strings.TrimSpace(r.Author), 100)

	if hasTitleVar && hasContentVar && titleText != "" && (strings.TrimSpace(contentSource) == strings.TrimSpace(r.Title) || strings.TrimSpace(contentSource) == titleText) {
		contentSource = ""
	}

	contentLimit := cfg.Media.SummaryLength
	if contentLimit <= 0 || contentLimit > 8000 {
		contentLimit = 8000
	}
	rawContent := ""
	if strings.TrimSpace(contentSource) != "" {
		rawContent = truncateRunes(contentSource, contentLimit)
	}

	styledTags, rawTags := formatTags(tags, cfg.Media.MaxTags)

	cleanURL := CleanTrackingURL(EffectiveSourceURL(r))
	displayURL := DisplayResultURL(cfg, r)

	rawPlatform := r.Platform
	if p, ok := model.PlatformByID(r.Platform); ok {
		rawPlatform = p.Name
	}
	rawType := typeLabel(r.Type)
	mediaStats := formatMediaStats(r, files)

	values := map[string]string{
		"{raw_title}":     rawTitle,
		"{raw_tags}":      rawTags,
		"{raw_author}":    rawAuthor,
		"{raw_content}":   rawContent,
		"{raw_url}":       displayURL,
		"{raw_clean_url}": cleanURL,
		"{raw_platform}":  rawPlatform,
		"{raw_type}":      rawType,
		"{media_stats}":   mediaStats,
		"{tags}":          styledTags,
	}
	if rawTitle != "" {
		values["{title}"] = "【" + rawTitle + "】"
	}
	if rawAuthor != "" {
		values["{author}"] = "作者：" + rawAuthor
	}
	values["{content}"] = rawContent
	if displayURL != "" {
		values["{url}"] = "链接：" + displayURL
	}
	if cleanURL != "" {
		values["{clean_url}"] = "链接：" + cleanURL
	}
	if rawPlatform != "" {
		values["{platform}"] = "[" + rawPlatform + "]"
	}
	if rawType != "" {
		values["{type}"] = "[" + rawType + "]"
	}

	var pairs []string
	for _, token := range templateTokenOrder {
		pairs = append(pairs, token, values[token])
	}
	replacer := strings.NewReplacer(pairs...)

	lines := strings.Split(tmpl, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		referenced := 0
		nonEmpty := 0
		for _, token := range templateTokenOrder {
			if strings.Contains(line, token) {
				referenced++
				if strings.TrimSpace(values[token]) != "" {
					nonEmpty++
				}
			}
		}
		if referenced > 0 {
			if nonEmpty == 0 {
				continue
			}
			rendered := replacer.Replace(line)
			if strings.TrimSpace(rendered) == "" {
				continue
			}
			out = append(out, rendered)
		} else {
			out = append(out, line)
		}
	}

	if strings.TrimSpace(r.DegradedReason) != "" && len(out) > 0 {
		out = append(out, strings.TrimSpace(r.DegradedReason))
	}

	joined := strings.Trim(strings.Join(out, "\n"), "\n")
	if strings.TrimSpace(joined) == "" {
		return ""
	}
	return joined
}
