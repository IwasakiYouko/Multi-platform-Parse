package policy

import (
	"fmt"
	"net/url"
	"strings"

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

func hostTrackingParam(host, lowerKey string) bool {
	switch {
	case host == "twitter.com" || strings.HasSuffix(host, ".twitter.com") || host == "x.com" || strings.HasSuffix(host, ".x.com") || host == "fixupx.com" || strings.HasSuffix(host, ".fixupx.com"):
		return lowerKey == "s" || lowerKey == "t"
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be":
		return lowerKey == "si" || lowerKey == "pp" || lowerKey == "feature"
	case host == "bilibili.com" || strings.HasSuffix(host, ".bilibili.com") || host == "b23.tv":
		return lowerKey == "mid" || lowerKey == "ts" || lowerKey == "timestamp" || lowerKey == "from"
	case host == "douyin.com" || strings.HasSuffix(host, ".douyin.com") || host == "iesdouyin.com" || strings.HasSuffix(host, ".iesdouyin.com") || host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com"):
		return lowerKey == "timestamp" || lowerKey == "app" || lowerKey == "sec_uid" || lowerKey == "checksum"
	}
	return false
}

// CleanTrackingURL strips common analytics and share-tracking query parameters
// while preserving functional parameters such as p, v, list, id, and xsec_token.
func CleanTrackingURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery == "" {
		return trimmed
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return trimmed
	}
	changed := false
	for key := range query {
		lk := strings.ToLower(key)
		if globalTrackingParams[lk] || hostTrackingParam(host, lk) {
			delete(query, key)
			changed = true
		}
	}
	if !changed {
		return trimmed
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// DisplayURL formats a source URL according to global CleanURL policy.
func DisplayURL(cfg model.Config, raw string) string {
	if cfg.Media.CleanURL {
		return CleanTrackingURL(raw)
	}
	return strings.TrimSpace(raw)
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

func typeLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "video":
		return "视频"
	case "image":
		return "图集"
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
	"{raw_type}",
	"{raw_url}",
	"{media_stats}",
	"{clean_url}",
	"{platform}",
	"{content}",
	"{author}",
	"{title}",
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

	rawTitle := truncateRunes(strings.TrimSpace(r.Title), 300)
	rawAuthor := truncateRunes(strings.TrimSpace(r.Author), 100)

	contentSource := r.Content
	if r.MarkdownContent != "" {
		contentSource = r.MarkdownContent
	}
	contentSource = strings.TrimRight(strings.ReplaceAll(contentSource, "\r\n", "\n"), "\n")

	hasTitleVar := strings.Contains(tmpl, "{title}") || strings.Contains(tmpl, "{raw_title}")
	hasContentVar := strings.Contains(tmpl, "{content}") || strings.Contains(tmpl, "{raw_content}")
	if hasTitleVar && hasContentVar && strings.TrimSpace(r.Title) != "" && strings.TrimSpace(contentSource) == strings.TrimSpace(r.Title) {
		contentSource = ""
	}
	rawContent := ""
	if strings.TrimSpace(contentSource) != "" {
		rawContent = truncateRunes(contentSource, 1800)
	}

	cleanURL := CleanTrackingURL(r.SourceURL)
	displayURL := DisplayURL(cfg, r.SourceURL)

	rawPlatform := r.Platform
	if p, ok := model.PlatformByID(r.Platform); ok {
		rawPlatform = p.Name
	}
	rawType := typeLabel(r.Type)
	mediaStats := formatMediaStats(r, files)

	values := map[string]string{
		"{raw_title}":     rawTitle,
		"{raw_author}":    rawAuthor,
		"{raw_content}":   rawContent,
		"{raw_url}":       displayURL,
		"{raw_clean_url}": cleanURL,
		"{raw_platform}":  rawPlatform,
		"{raw_type}":      rawType,
		"{media_stats}":   mediaStats,
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

	joined := strings.Trim(strings.Join(out, "\n"), "\n")
	if strings.TrimSpace(joined) == "" {
		return ""
	}
	return joined
}
