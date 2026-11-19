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
	"utm_source":             true,
	"utm_medium":             true,
	"utm_campaign":           true,
	"utm_term":               true,
	"utm_content":            true,
	"utm_id":                 true,
	"utm_psn":                true,
	"fbclid":                 true,
	"gclid":                  true,
	"msclkid":                true,
	"ttclid":                 true,
	"twclid":                 true,
	"yclid":                  true,
	"wickedid":               true,
	"li_fat_id":              true,
	"mc_cid":                 true,
	"mc_eid":                 true,
	"mkt_tok":                true,
	"_hsenc":                 true,
	"_hsmi":                  true,
	"s_kwcid":                true,
	"igshid":                 true,
	"igsh":                   true,
	"xmt":                    true,
	"vd_source":              true,
	"spm":                    true,
	"scm":                    true,
	"spm_id_from":            true,
	"from_spmid":             true,
	"from_source":            true,
	"from":                   true,
	"isappinstalled":         true,
	"nsukey":                 true,
	"seid":                   true,
	"share_source":           true,
	"share_medium":           true,
	"share_plat":             true,
	"share_session_id":       true,
	"share_from":             true,
	"share_tag":              true,
	"share_copy_link":        true,
	"share_token":            true,
	"sharer_shareinfo":       true,
	"sharer_shareinfo_first": true,
	"mpshare":                true,
	"chksm":                  true,
	"unique_k":               true,
	"bbid":                   true,
	"buvid":                  true,
	"start_progress":         true,
	"is_story_h5":            true,
	"up_id":                  true,
	"plat_id":                true,
	"tt_from":                true,
	"u_code":                 true,
	"iid":                    true,
	"did":                    true,
	"enter_from":             true,
	"enter_method":           true,
	"is_from_webapp":         true,
	"sender_device":          true,
	"web_id":                 true,
	"_r":                     true,
	"_t":                     true,
	"share_app_id":           true,
	"share_link_id":          true,
	"share_item_id":          true,
	"ug_btm":                 true,
	"ugbiz_name":             true,
	"xhsshare":               true,
	"xsec_token":             true,
	"xsec_source":            true,
	"author_share":           true,
	"apptime":                true,
	"appuid":                 true,
	"shareredid":             true,
	"si":                     true,
	"pp":                     true,
	"feature":                true,
}

var (
	biliDynamicPathRe   = regexp.MustCompile(`^/(?:opus/|dynamic/)?([0-9]{15,22})(?:/|$)`)
	biliReadMobileRe    = regexp.MustCompile(`^/read/mobile/([0-9]+)(?:/|$)`)
	douyinVideoPathRe   = regexp.MustCompile(`^/(?:share/)?video/([0-9]+)(?:/|$)`)
	douyinNotePathRe    = regexp.MustCompile(`^/(?:share/)?(?:note|slides)/([0-9]+)(?:/|$)`)
	xhsProfileNoteRe    = regexp.MustCompile(`^/user/profile/[^/?#]+/([0-9a-fA-F]{16,32})(?:/|$)`)
	ksPhotoPathRe       = regexp.MustCompile(`^/(?:short-video|fw/photo)/([^/?#]+)(?:/|$)`)
	tiebaPostPathRe     = regexp.MustCompile(`^/p/([0-9]+)(?:/|$)`)
	weixinSphPathRe     = regexp.MustCompile(`^/sph/([A-Za-z0-9_-]+)(?:/|$)`)
	xhhLinkPathRe       = regexp.MustCompile(`/link/([A-Za-z0-9_-]+)(?:/|$)`)
	xhhSafeIDRe         = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	zhihuAppviewPRe     = regexp.MustCompile(`^/(?:appview/)?p/([0-9]+)(?:/|$)`)
	toutiaoItemPathRe   = regexp.MustCompile(`^/(?:(?:group|item)/|i|a)([0-9]+)(?:/|$)`)
	tiktokMobileVPathRe = regexp.MustCompile(`^/v/([0-9]+)(?:\.html)?(?:/|$)`)
	zuiyouDetailPathRe  = regexp.MustCompile(`^/detail/([0-9]+)(?:/|$)`)
	digitsOnlyRe        = regexp.MustCompile(`^[0-9]+$`)
	inlineHTTPURLRe     = regexp.MustCompile(`https?://[^\s<>"'()（）【】「」『』《》，。；！？、：]+`)
	multiBlankLinesRe   = regexp.MustCompile(`(?:\n[ \t]*){3,}`)

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
		if lk == "see_lz" {
			if len(vals) == 1 && (vals[0] == "" || vals[0] == "0" || strings.EqualFold(vals[0], "false")) {
				continue
			}
		}
		if lk == "t" {
			if len(vals) == 1 && (vals[0] == "" || vals[0] == "0" || vals[0] == "0.0" || vals[0] == "0s") {
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
		u.Scheme = "https"
		u.Fragment = ""
		u.RawQuery = allowQueryKeys(q, "p")
		return u.String()

	case matchDomain(host, "bilibili.com"):
		u.Scheme = "https"
		u.Fragment = ""
		if host == "t.bilibili.com" || strings.HasPrefix(u.Path, "/opus/") || strings.HasPrefix(u.Path, "/dynamic/") {
			if m := biliDynamicPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
				return "https://www.bilibili.com/opus/" + m[1]
			}
		}
		if m := biliReadMobileRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://www.bilibili.com/read/cv" + m[1]
		}
		if u.Path == "/read/mobile" && digitsOnlyRe.MatchString(q.Get("id")) {
			return "https://www.bilibili.com/read/cv" + q.Get("id")
		}
		if host == "m.bilibili.com" || host == "bilibili.com" {
			u.Host = "www.bilibili.com"
		}
		if strings.HasPrefix(u.Path, "/video/") {
			u.Path = strings.TrimRight(u.Path, "/")
			u.RawQuery = allowQueryKeys(q, "p", "t")
		} else {
			u.Path = strings.TrimRight(u.Path, "/")
			u.RawQuery = ""
		}
		return u.String()

	case matchDomain(host, "douyin.com", "iesdouyin.com"):
		u.Scheme = "https"
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
		for _, k := range []string{"modal_id", "vid", "aweme_id"} {
			if id := q.Get(k); digitsOnlyRe.MatchString(id) {
				return "https://www.douyin.com/video/" + id
			}
		}
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "xhslink.com", "xhslink.cn"):
		u.Scheme = "https"
		u.Fragment = ""
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "xiaohongshu.com"):
		u.Scheme = "https"
		u.Host = "www.xiaohongshu.com"
		u.Fragment = ""
		if strings.HasPrefix(u.Path, "/discovery/item/") {
			id := strings.Trim(strings.TrimPrefix(u.Path, "/discovery/item/"), "/")
			if id != "" && !strings.Contains(id, "/") {
				u.Path = "/explore/" + id
			}
		} else if m := xhsProfileNoteRe.FindStringSubmatch(u.Path); len(m) == 2 {
			u.Path = "/explore/" + m[1]
		} else if strings.HasPrefix(u.Path, "/explore/") {
			u.Path = strings.TrimRight(u.Path, "/")
		}
		// Strip 100% of query parameters (including xsec_source and xsec_token) for pure direct URLs.
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "kuaishou.com", "chenzhongtech.com", "gifshow.com"):
		u.Scheme = "https"
		u.Fragment = ""
		if m := ksPhotoPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://www.kuaishou.com/short-video/" + strings.TrimSuffix(m[1], ".html")
		}
		if pid := strings.TrimSpace(q.Get("photoId")); xhhSafeIDRe.MatchString(pid) {
			return "https://www.kuaishou.com/short-video/" + pid
		}
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "weibo.com", "weibo.cn"):
		u.Scheme = "https"
		u.Fragment = ""
		if strings.HasPrefix(u.Path, "/show") {
			if fid := strings.TrimSpace(q.Get("fid")); fid != "" && !strings.ContainsAny(fid, " /?#&") {
				return "https://weibo.com/tv/show/" + fid
			}
			u.RawQuery = allowQueryKeys(q, "fid")
		} else {
			if host == "weibo.com" || host == "www.weibo.com" {
				u.Host = "weibo.com"
			}
			u.Path = strings.TrimRight(u.Path, "/")
			u.RawQuery = ""
		}
		return u.String()

	case matchDomain(host, "tieba.baidu.com", "tiebap.baidu.com"):
		u.Scheme = "https"
		u.Host = "tieba.baidu.com"
		u.Fragment = ""
		if m := tiebaPostPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			u.Path = "/p/" + m[1]
		}
		u.RawQuery = allowQueryKeys(q, "see_lz", "pn")
		return u.String()

	case matchDomain(host, "mp.weixin.qq.com"):
		u.Scheme = "https"
		u.Fragment = ""
		if strings.HasPrefix(u.Path, "/s/") && len(u.Path) > 3 {
			u.RawQuery = ""
		} else {
			u.RawQuery = allowQueryKeys(q, "__biz", "mid", "idx", "sn")
		}
		return u.String()

	case matchDomain(host, "weixin.qq.com", "channels.weixin.qq.com", "finder.video.qq.com"):
		u.Scheme = "https"
		u.Fragment = ""
		if m := weixinSphPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://weixin.qq.com/sph/" + m[1]
		}
		if u.Path == "/finder-preview/pages/sph" && xhhSafeIDRe.MatchString(q.Get("id")) {
			return "https://weixin.qq.com/sph/" + q.Get("id")
		}
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "twitter.com", "x.com", "fixupx.com"):
		u.Scheme = "https"
		u.Host = "x.com"
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()

	case matchDomain(host, "instagram.com", "instagr.am"):
		u.Scheme = "https"
		u.Host = "www.instagram.com"
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()

	case matchDomain(host, "threads.net", "threads.com"):
		u.Scheme = "https"
		u.Host = "www.threads.com"
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()

	case matchDomain(host, "tiktok.com"):
		u.Scheme = "https"
		u.Fragment = ""
		u.RawQuery = ""
		if m := tiktokMobileVPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://www.tiktok.com/@_/video/" + m[1]
		}
		return u.String()

	case matchDomain(host, "zhihu.com", "zhihu.link"):
		u.Scheme = "https"
		u.Fragment = ""
		u.RawQuery = ""
		if matchDomain(host, "zhihu.link") {
			return u.String()
		}
		if m := zhihuAppviewPRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://zhuanlan.zhihu.com/p/" + m[1]
		}
		if host == "m.zhihu.com" || host == "zhihu.com" {
			u.Host = "www.zhihu.com"
		}
		u.Path = strings.TrimRight(u.Path, "/")
		return u.String()

	case matchDomain(host, "coolapk.com", "coolapk1s.com"):
		u.Scheme = "https"
		u.Host = "www.coolapk.com"
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()

	case matchDomain(host, "toutiao.com"):
		u.Scheme = "https"
		u.Fragment = ""
		u.RawQuery = ""
		if m := toutiaoItemPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			return "https://www.toutiao.com/article/" + m[1] + "/"
		}
		if host == "m.toutiao.com" || host == "toutiao.com" {
			u.Host = "www.toutiao.com"
		}
		return u.String()

	case matchDomain(host, "bgm.tv", "bangumi.tv", "chii.in"):
		u.Scheme = "https"
		u.Host = "bgm.tv"
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()

	case matchDomain(host, "t.co", "pipix.com", "medium.com", "dou.bz", "douc.cc"):
		u.Scheme = "https"
		u.Fragment = ""
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "douban.com"):
		u.Scheme = "https"
		u.Fragment = ""
		if host == "m.douban.com" || host == "douban.com" {
			u.Host = "www.douban.com"
		}
		if strings.HasPrefix(u.Path, "/doubanapp/dispatch") {
			if rawURI := strings.TrimSpace(q.Get("uri")); rawURI != "" {
				if parsedURI, pErr := url.Parse(rawURI); pErr == nil && parsedURI.Host == "" && parsedURI.Path != "" {
					cleanPath := "/" + strings.TrimLeft(parsedURI.Path, "/")
					return "https://www.douban.com" + cleanPath
				}
			}
		}
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "xiaoheihe.cn", "xiaoheihe.com"):
		id := strings.TrimSpace(q.Get("link_id"))
		if id == "" {
			if m := xhhLinkPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
				id = m[1]
			}
		}
		if id != "" && xhhSafeIDRe.MatchString(id) {
			return "https://www.xiaoheihe.cn/app/bbs/link/" + id
		}
		u.Scheme = "https"
		u.Host = "www.xiaoheihe.cn"
		u.Fragment = ""
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "izuiyou.com", "xiaochuankeji.cn"):
		u.Scheme = "https"
		u.Fragment = ""
		rawPid := ""
		if pids := q["pid"]; len(pids) > 0 {
			rawPid = strings.TrimSpace(pids[len(pids)-1])
		} else if m := zuiyouDetailPathRe.FindStringSubmatch(u.Path); len(m) == 2 {
			rawPid = m[1]
		}
		if rawPid != "" && digitsOnlyRe.MatchString(rawPid) {
			pid := strings.TrimLeft(rawPid, "0")
			if pid == "" {
				pid = "0"
			}
			return "https://share.xiaochuankeji.cn/hybrid/share/post?pid=" + pid
		}
		u.RawQuery = ""
		return u.String()

	case matchDomain(host, "youtube.com", "youtu.be"):
		u.Scheme = "https"
		u.Fragment = ""
		if host == "youtu.be" {
			vid := strings.Trim(u.Path, "/")
			if vid != "" && !strings.Contains(vid, "/") {
				watch := &url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/watch"}
				wq := url.Values{"v": {vid}}
				if tVal := q.Get("t"); tVal != "" && tVal != "0" && tVal != "0s" {
					wq.Set("t", tVal)
				}
				watch.RawQuery = wq.Encode()
				return watch.String()
			}
			u.RawQuery = allowQueryKeys(q, "t")
			return u.String()
		}
		if host == "m.youtube.com" || host == "youtube.com" {
			u.Host = "www.youtube.com"
		}
		if strings.HasPrefix(u.Path, "/watch") {
			u.RawQuery = allowQueryKeys(q, "v", "t")
		} else if strings.HasPrefix(u.Path, "/playlist") {
			u.RawQuery = allowQueryKeys(q, "list")
		} else if strings.HasPrefix(u.Path, "/shorts/") {
			u.Path = strings.TrimRight(u.Path, "/")
			u.RawQuery = ""
		} else {
			u.RawQuery = allowQueryKeys(q, "t")
		}
		return u.String()

	case matchDomain(host, "facebook.com", "fb.watch", "fb.com"):
		u.Scheme = "https"
		u.Fragment = ""
		if host == "fb.watch" {
			u.RawQuery = ""
			return u.String()
		}
		if host == "facebook.com" || host == "m.facebook.com" || host == "fb.com" || host == "www.fb.com" {
			u.Host = "www.facebook.com"
		}
		u.RawQuery = allowQueryKeys(q, "v", "id", "story_fbid", "fbid")
		return u.String()

	case matchDomain(host, "snapchat.com"):
		u.Scheme = "https"
		u.Host = "www.snapchat.com"
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawQuery = ""
		u.Fragment = ""
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
// cooldowns and cached works across distinct share links.
func WorkIdentityURL(raw string) string {
	return CleanTrackingURL(raw)
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
		base := CleanTrackingURL(EffectiveSourceURL(r))
		if cfg.Media.XHSKeepToken && r.Platform == "xhs" && base != "" {
			if su, err := url.Parse(strings.TrimSpace(r.SourceURL)); err == nil {
				if tok := strings.TrimSpace(su.Query().Get("xsec_token")); tok != "" {
					if bu, bErr := url.Parse(base); bErr == nil {
						bq := url.Values{"xsec_token": {tok}}
						bu.RawQuery = bq.Encode()
						return bu.String()
					}
				}
			}
		}
		return base
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

func formatMediaDuration(r model.ParseResult, files []model.Artifact) string {
	dur := 0.0
	for _, f := range files {
		if (f.Kind == "video" || f.Kind == "audio") && f.Duration > 0 {
			dur = f.Duration
			break
		}
	}
	if dur <= 0 {
		for _, m := range r.Media {
			if (m.Kind == "video" || m.Kind == "audio") && m.Duration > 0 {
				dur = m.Duration
				break
			}
		}
	}
	total := int(dur + 0.5)
	if total <= 0 {
		return ""
	}
	h := total / 3600
	m := (total % 3600) / 60
	sec := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

func sanitizeDisplayText(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\r', '\u200b', '\u200c', '\ufeff', '\u2060', '\u00ad':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return multiBlankLinesRe.ReplaceAllString(b.String(), "\n\n")
}

func cleanInlineURLs(s string) string {
	if !strings.Contains(s, "http://") && !strings.Contains(s, "https://") {
		return s
	}
	return inlineHTTPURLRe.ReplaceAllStringFunc(s, func(matched string) string {
		trimmed := strings.TrimRight(matched, ".,;:!?]")
		suffix := matched[len(trimmed):]
		if trimmed == "" || trimmed == "http://" || trimmed == "https://" {
			return matched
		}
		return CleanTrackingURL(trimmed) + suffix
	})
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
	"{raw_duration}",
	"{raw_platform}",
	"{raw_content}",
	"{raw_author}",
	"{raw_title}",
	"{raw_tags}",
	"{raw_type}",
	"{raw_url}",
	"{media_stats}",
	"{clean_url}",
	"{duration}",
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

func prepareCleanedBody(cfg model.Config, r model.ParseResult, stripTags bool) ([]string, string, string) {
	cleanTitleIn := sanitizeDisplayText(r.Title)
	_, cleanedTitle := ExtractAndCleanTags(nil, cleanTitleIn)
	if cleanedTitle == "" {
		cleanedTitle = strings.TrimSpace(cleanTitleIn)
	}

	contentSource := r.Content
	isMarkdown := false
	if r.MarkdownContent != "" {
		contentSource = r.MarkdownContent
		isMarkdown = true
	}
	contentSource = strings.TrimRight(sanitizeDisplayText(contentSource), "\n")

	var tags []string
	cleanedContent := contentSource
	if isMarkdown {
		tags, _ = ExtractAndCleanTags(r.Tags, sanitizeDisplayText(r.Content))
	} else if stripTags {
		tags, cleanedContent = ExtractAndCleanTags(r.Tags, contentSource)
	} else {
		tags, _ = ExtractAndCleanTags(r.Tags, contentSource)
	}
	if cfg.Media.CleanURL && cleanedContent != "" {
		cleanedContent = cleanInlineURLs(cleanedContent)
	}
	return tags, cleanedTitle, cleanedContent
}

// FullTextForForward returns the cleaned content (up to 8,000 runes) and whether Message 1 truncated it.
func FullTextForForward(cfg model.Config, r model.ParseResult) (string, bool) {
	return FullTextForForwardWithTemplate(cfg, ResolveMessageTemplate(cfg, r.Platform), r)
}

// FullTextForForwardWithTemplate evaluates full-text forward eligibility against an explicit template.
func FullTextForForwardWithTemplate(cfg model.Config, tmpl string, r model.ParseResult) (string, bool) {
	if !strings.Contains(tmpl, "{content}") && !strings.Contains(tmpl, "{raw_content}") {
		return "", false
	}
	hasTagsVar := strings.Contains(tmpl, "{tags}") || strings.Contains(tmpl, "{raw_tags}")
	_, cleanedTitle, cleanedContent := prepareCleanedBody(cfg, r, hasTagsVar)
	if strings.TrimSpace(cleanedContent) == "" {
		return "", false
	}
	hasTitleVar := strings.Contains(tmpl, "{title}") || strings.Contains(tmpl, "{raw_title}")
	rawCleanTitle := strings.TrimSpace(sanitizeDisplayText(r.Title))
	if hasTitleVar && (strings.TrimSpace(cleanedContent) == rawCleanTitle || strings.TrimSpace(cleanedContent) == strings.TrimSpace(cleanedTitle)) {
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

// ForwardTextChunks splits truncated long-form content on natural paragraph/line boundaries
// into comfortable chat nodes (<= 1,500 runes per node) for Merged Forward reading.
func ForwardTextChunks(cfg model.Config, r model.ParseResult) []string {
	return ForwardTextChunksWithTemplate(cfg, ResolveMessageTemplate(cfg, r.Platform), r)
}

// ForwardTextChunksWithTemplate splits truncated long-form content using an explicit template.
func ForwardTextChunksWithTemplate(cfg model.Config, tmpl string, r model.ParseResult) []string {
	fullText, truncated := FullTextForForwardWithTemplate(cfg, tmpl, r)
	if !truncated || strings.TrimSpace(fullText) == "" {
		return nil
	}
	const maxChunkRunes = 1500
	runes := []rune(fullText)
	if len(runes) <= maxChunkRunes {
		return []string{fullText}
	}
	paragraphs := strings.Split(fullText, "\n")
	var chunks []string
	var cur strings.Builder
	curRunes := 0
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
		curRunes = 0
	}
	for _, p := range paragraphs {
		pRunes := []rune(p)
		for len(pRunes) > maxChunkRunes {
			if curRunes > 0 {
				flush()
			}
			chunks = append(chunks, string(pRunes[:maxChunkRunes]))
			pRunes = pRunes[maxChunkRunes:]
		}
		needed := len(pRunes)
		if curRunes > 0 {
			needed++ // newline
		}
		if curRunes > 0 && curRunes+needed > maxChunkRunes {
			flush()
		}
		if curRunes > 0 {
			cur.WriteByte('\n')
			curRunes++
		}
		cur.WriteString(string(pRunes))
		curRunes += len(pRunes)
	}
	flush()
	return chunks
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

	tags, cleanedTitle, contentSource := prepareCleanedBody(cfg, r, hasTagsVar)

	rawInputTitle := strings.TrimSpace(sanitizeDisplayText(r.Title))
	rawInputContent := strings.TrimSpace(sanitizeDisplayText(r.Content))
	titleText := rawInputTitle
	if hasTagsVar && cleanedTitle != "" && rawInputContent == rawInputTitle {
		titleText = cleanedTitle
	}
	rawTitle := truncateRunes(titleText, 300)
	rawAuthor := truncateRunes(strings.TrimSpace(sanitizeDisplayText(r.Author)), 100)

	if hasTitleVar && hasContentVar && titleText != "" && (strings.TrimSpace(contentSource) == rawInputTitle || strings.TrimSpace(contentSource) == titleText) {
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
	rawDuration := formatMediaDuration(r, files)

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
		"{raw_duration}":  rawDuration,
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
	if rawDuration != "" {
		values["{duration}"] = "时长：" + rawDuration
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
