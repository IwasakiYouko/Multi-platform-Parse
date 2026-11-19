package model

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const CurrentConfigSchema = 2

type Config struct {
	SchemaVersion int                       `json:"schema_version,omitempty"`
	Server        ServerConfig              `json:"server"`
	OneBot        OneBotConfig              `json:"onebot"`
	Security      SecurityConfig            `json:"security"`
	Proxy         ProxyConfig               `json:"proxy"`
	Media         MediaConfig               `json:"media"`
	Cache         CacheConfig               `json:"cache"`
	Policy        PolicyConfig              `json:"policy"`
	Platforms     map[string]PlatformPolicy `json:"platforms"`
}
type ServerConfig struct {
	Listen         string   `json:"listen"`
	Port           int      `json:"port"`
	ExternalURL    string   `json:"external_url"`
	TLS            bool     `json:"tls"`
	CertFile       string   `json:"cert_file"`
	KeyFile        string   `json:"key_file"`
	TrustedProxies []string `json:"trusted_proxies"`
	AdminNetworks  []string `json:"admin_networks"`
}
type OneBotConfig struct {
	Token   string   `json:"token"`
	SelfIDs []string `json:"self_ids"`
}
type SecurityConfig struct {
	Failures      int `json:"failures"`
	WindowMinutes int `json:"window_minutes"`
	BanMinutes    int `json:"ban_minutes"`
	SessionHours  int `json:"session_hours"`
}
type ProxyConfig struct {
	Mode string `json:"mode"`
	URL  string `json:"url"`
}
type MediaConfig struct {
	MaxBytes             int64  `json:"max_bytes"`
	MaxDurationSeconds   int    `json:"max_duration_seconds"`
	MaxImages            int    `json:"max_images"`
	Quality              int    `json:"quality"`
	Concurrency          int    `json:"concurrency"`
	TranscodeConcurrency int    `json:"transcode_concurrency"`
	MaxDiskBytes         int64  `json:"max_disk_bytes"`
	AutoDownload         bool   `json:"auto_download"`
	ShowCover            bool   `json:"show_cover"`
	ShowInfo             bool   `json:"show_info"`
	CleanURL             bool   `json:"clean_url"`
	ForwardEnabled       bool   `json:"forward_enabled"`
	SummaryLength        int    `json:"summary_length"`
	MaxTags              int    `json:"max_tags"`
	MessageTemplate      string `json:"message_template"`
	OversizeMode         string `json:"oversize_mode"`
	Transport            string `json:"transport"`
}
type CacheConfig struct {
	TTLHours int `json:"ttl_hours"`
}
type PolicyConfig struct {
	Enabled          bool    `json:"enabled"`
	PrivateEnabled   bool    `json:"private_enabled"`
	GroupReply       bool    `json:"group_reply"`
	EmojiReaction    bool    `json:"emoji_reaction"`
	GroupSilentError bool    `json:"group_silent_error"`
	MaxLinksPerMsg   int     `json:"max_links_per_msg"`
	GroupWhitelist   []int64 `json:"group_whitelist"`
	GroupBlacklist   []int64 `json:"group_blacklist"`
	RejectText       string  `json:"reject_text"`
	CooldownSeconds  int     `json:"cooldown_seconds"`
}
type PlatformPolicy struct {
	Enabled         bool   `json:"enabled"`
	NSFWBlocked     bool   `json:"nsfw_blocked"`
	Overseas        bool   `json:"overseas"`
	Cookie          string `json:"cookie"`
	RejectText      string `json:"reject_text"`
	Silent          bool   `json:"silent"`
	TemplateMode    string `json:"template_mode"`
	MessageTemplate string `json:"message_template"`
	CoverMode       string `json:"cover_mode"`
}

const DefaultMessageTemplate = "{title}\n{tags}\n{content}\n{url}"
const LegacyMessageTemplateV20 = "{title}\n{author}\n{content}\n{url}"
type Platform struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Overseas bool     `json:"overseas"`
	Domains  []string `json:"domains"`
	Types    []string `json:"types"`
}

var Platforms = []Platform{
	{"bilibili", "哔哩哔哩", false, []string{"bilibili.com", "b23.tv", "bili2233.cn"}, []string{"video", "image", "article"}},
	{"douyin", "抖音", false, []string{"douyin.com", "iesdouyin.com"}, []string{"video", "image"}},
	{"tiktok", "TikTok", true, []string{"tiktok.com"}, []string{"video", "image"}},
	{"xhs", "小红书", false, []string{"xiaohongshu.com", "xhslink.com", "xhslink.cn"}, []string{"video", "image"}},
	{"weibo", "微博", false, []string{"weibo.com", "weibo.cn"}, []string{"video", "image"}},
	{"kuaishou", "快手", false, []string{"kuaishou.com", "gifshow.com", "chenzhongtech.com"}, []string{"video", "image"}},
	{"twitter", "Twitter / X", true, []string{"twitter.com", "x.com", "t.co", "fixupx.com"}, []string{"video", "image", "article"}},
	{"instagram", "Instagram", true, []string{"instagram.com", "instagr.am"}, []string{"video", "image"}},
	{"youtube", "YouTube", true, []string{"youtube.com", "youtu.be", "music.youtube.com"}, []string{"video", "audio"}},
	{"facebook", "Facebook", true, []string{"facebook.com", "fb.watch", "fb.com"}, []string{"video"}},
	{"threads", "Threads", true, []string{"threads.net", "threads.com"}, []string{"video", "image"}},
	{"tieba", "贴吧", false, []string{"tieba.baidu.com", "tiebap.baidu.com"}, []string{"video", "image"}},
	{"weixin", "微信公众号", false, []string{"mp.weixin.qq.com"}, []string{"article", "image"}},
	{"weixin_channels", "微信视频号", false, []string{"channels.weixin.qq.com", "weixin.qq.com", "finder.video.qq.com"}, []string{"video"}},
	{"coolapk", "酷安", false, []string{"coolapk.com", "coolapk1s.com"}, []string{"image", "article"}},
	{"pipix", "皮皮虾", false, []string{"pipix.com", "h5.pipix.com"}, []string{"video", "image"}},
	{"zuiyou", "最右", false, []string{"izuiyou.com", "xiaochuankeji.cn"}, []string{"video", "image"}},
	{"xiaoheihe", "小黑盒", false, []string{"xiaoheihe.cn", "xiaoheihe.com", "api.xiaoheihe.cn"}, []string{"video", "image"}},
	{"snapchat", "Snapchat", true, []string{"snapchat.com"}, []string{"video"}},
	{"zhihu", "知乎", false, []string{"zhihu.com", "zhihu.link"}, []string{"video", "image", "article"}},
	{"douban", "豆瓣", false, []string{"douban.com", "doubanio.com", "dou.bz", "douc.cc"}, []string{"video", "image", "article"}},
	{"bangumi", "Bangumi", false, []string{"bgm.tv", "bangumi.tv", "chii.in"}, []string{"image", "article"}},
	{"toutiao", "今日头条", false, []string{"toutiao.com", "toutiaoimg.cn", "toutiaocdn.com"}, []string{"video", "image", "article"}},
	{"medium", "Medium", true, []string{"medium.com"}, []string{"image", "article", "video"}},
}

func PlatformByID(id string) (Platform, bool) {
	for _, p := range Platforms {
		if p.ID == id {
			return p, true
		}
	}
	return Platform{}, false
}

// BilibiliURL recognizes bare BV/AV identifiers without confusing them with hosts.
// Query parameters (including the selected P) remain available to the resolver.
func BilibiliURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || strings.Contains(u.Path, "/") {
		return "", false
	}
	id := u.Path
	if len(id) == 12 && strings.EqualFold(id[:3], "BV1") {
		for _, c := range id[3:] {
			if !strings.ContainsRune("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz", c) {
				return "", false
			}
		}
		id = "BV" + id[2:]
	} else if len(id) > 2 && strings.EqualFold(id[:2], "av") {
		for _, c := range id[2:] {
			if c < '0' || c > '9' {
				return "", false
			}
		}
		aid, e := strconv.ParseUint(id[2:], 10, 63)
		if e != nil || aid == 0 {
			return "", false
		}
		id = "av" + strconv.FormatUint(aid, 10)
	} else {
		return "", false
	}
	result := url.URL{Scheme: "https", Host: "www.bilibili.com", Path: "/video/" + id, RawQuery: u.RawQuery}
	return result.String(), true
}
func Identify(raw string) (Platform, bool) {
	raw = strings.TrimSpace(raw)
	if _, ok := BilibiliURL(raw); ok {
		return PlatformByID("bilibili")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, e := url.Parse(raw)
	if e != nil {
		return Platform{}, false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, p := range Platforms {
		for _, d := range p.Domains {
			if host == d || strings.HasSuffix(host, "."+d) {
				return p, true
			}
		}
	}
	return Platform{}, false
}
func NewID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func DefaultConfig() Config {
	p := map[string]PlatformPolicy{}
	for _, v := range Platforms {
		p[v.ID] = PlatformPolicy{Enabled: true, Overseas: v.Overseas, TemplateMode: "inherit", CoverMode: "inherit"}
	}
	return Config{
		SchemaVersion: CurrentConfigSchema,
		Server:        ServerConfig{Listen: "::", Port: 10492, TrustedProxies: []string{}, AdminNetworks: []string{}},
		OneBot:        OneBotConfig{Token: NewID() + NewID(), SelfIDs: []string{}},
		Security:      SecurityConfig{Failures: 5, WindowMinutes: 15, BanMinutes: 60, SessionHours: 12},
		Proxy:         ProxyConfig{Mode: "direct"},
		Media:         MediaConfig{MaxBytes: 80 * 1024 * 1024, MaxDurationSeconds: 1500, MaxImages: 30, Quality: 720, Concurrency: 2, TranscodeConcurrency: 1, MaxDiskBytes: 2 * 1024 * 1024 * 1024, AutoDownload: true, ShowCover: true, ShowInfo: true, CleanURL: true, ForwardEnabled: true, SummaryLength: 300, MaxTags: 8, MessageTemplate: DefaultMessageTemplate, OversizeMode: "link", Transport: "file"},
		Cache:         CacheConfig{TTLHours: 24},
		Policy:        PolicyConfig{Enabled: true, PrivateEnabled: true, GroupReply: true, EmojiReaction: true, GroupSilentError: false, MaxLinksPerMsg: 3, CooldownSeconds: 30, RejectText: "{platform} 暂不支持解析：{reason}", GroupWhitelist: []int64{}, GroupBlacklist: []int64{}},
		Platforms:     p,
	}
}

// NormalizeConfig fills backward-compatible defaults on upgraded or imported configurations.
func NormalizeConfig(c *Config) {
	if c == nil {
		return
	}
	if c.Server.TrustedProxies == nil {
		c.Server.TrustedProxies = []string{}
	}
	if c.Server.AdminNetworks == nil {
		c.Server.AdminNetworks = []string{}
	}
	if c.OneBot.SelfIDs == nil {
		c.OneBot.SelfIDs = []string{}
	}
	if c.Policy.GroupWhitelist == nil {
		c.Policy.GroupWhitelist = []int64{}
	}
	if c.Policy.GroupBlacklist == nil {
		c.Policy.GroupBlacklist = []int64{}
	}
	if c.SchemaVersion < CurrentConfigSchema {
		c.Media.CleanURL = true
		c.Media.ForwardEnabled = true
		if c.Media.SummaryLength == 0 {
			c.Media.SummaryLength = 300
		}
		if c.Media.MaxTags == 0 {
			c.Media.MaxTags = 8
		}
		if strings.TrimSpace(c.Media.MessageTemplate) == "" || strings.TrimSpace(c.Media.MessageTemplate) == LegacyMessageTemplateV20 {
			c.Media.MessageTemplate = DefaultMessageTemplate
		}
		c.SchemaVersion = CurrentConfigSchema
	}
	if c.Policy.MaxLinksPerMsg <= 0 {
		c.Policy.MaxLinksPerMsg = 3
	}
	if c.Media.SummaryLength < 0 {
		c.Media.SummaryLength = 0
	}
	if c.Media.MaxTags < 0 {
		c.Media.MaxTags = 0
	}
	if strings.TrimSpace(c.Media.MessageTemplate) == "" {
		c.Media.MessageTemplate = DefaultMessageTemplate
	}
	if c.Platforms == nil {
		c.Platforms = map[string]PlatformPolicy{}
	}
	for _, def := range Platforms {
		pol, ok := c.Platforms[def.ID]
		if !ok {
			pol = PlatformPolicy{Enabled: true, Overseas: def.Overseas, TemplateMode: "inherit", CoverMode: "inherit"}
		} else {
			if pol.TemplateMode == "" {
				pol.TemplateMode = "inherit"
			}
			if pol.CoverMode == "" {
				pol.CoverMode = "inherit"
			}
		}
		c.Platforms[def.ID] = pol
	}
}

func (c Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("端口必须为1–65535")
	}
	if strings.TrimSpace(c.OneBot.Token) == "" || len(c.OneBot.Token) < 24 {
		return fmt.Errorf("OneBot Token至少24字符")
	}
	if c.Cache.TTLHours < 1 || c.Cache.TTLHours > 8760 {
		return fmt.Errorf("缓存期限必须为1–8760小时")
	}
	if c.Policy.CooldownSeconds < 0 || c.Policy.CooldownSeconds > 86400 {
		return fmt.Errorf("会话冷却必须为0–86400秒")
	}
	if c.Policy.MaxLinksPerMsg < 1 || c.Policy.MaxLinksPerMsg > 32 {
		return fmt.Errorf("单条消息最大链接数必须为1–32")
	}
	if c.Security.Failures < 1 || c.Security.Failures > 100 || c.Security.WindowMinutes < 1 || c.Security.WindowMinutes > 1440 || c.Security.BanMinutes < 1 || c.Security.BanMinutes > 43200 || c.Security.SessionHours < 1 || c.Security.SessionHours > 168 {
		return fmt.Errorf("登录安全参数超出范围")
	}
	if c.Media.MaxBytes < 1024 || c.Media.MaxBytes > 4*1024*1024*1024 || c.Media.MaxDiskBytes < c.Media.MaxBytes || c.Media.MaxImages < 1 || c.Media.MaxImages > 200 || c.Media.MaxDurationSeconds < 1 || c.Media.MaxDurationSeconds > 86400 || c.Media.Concurrency < 1 || c.Media.Concurrency > 16 || c.Media.TranscodeConcurrency < 1 || c.Media.TranscodeConcurrency > 4 || c.Media.SummaryLength < 0 || c.Media.SummaryLength > 4000 || c.Media.MaxTags < 0 || c.Media.MaxTags > 50 {
		return fmt.Errorf("媒体资源限制无效")
	}
	if len(c.Media.MessageTemplate) > 4096 {
		return fmt.Errorf("全局消息模板不能超过4096字符")
	}
	if c.Media.OversizeMode != "link" && c.Media.OversizeMode != "file" {
		return fmt.Errorf("超限处理仅支持link/file")
	}
	if c.Media.Transport != "file" && c.Media.Transport != "http" {
		return fmt.Errorf("媒体传输仅支持file/http")
	}
	if c.Media.Transport == "http" {
		u, e := url.Parse(c.Server.ExternalURL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return fmt.Errorf("HTTP媒体传输需要有效的对外访问地址")
		}
	}
	if c.Proxy.Mode != "direct" && c.Proxy.Mode != "all" && c.Proxy.Mode != "overseas" {
		return fmt.Errorf("未知代理模式")
	}
	if c.Proxy.URL != "" {
		u, e := url.Parse(c.Proxy.URL)
		if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return fmt.Errorf("代理地址必须是HTTP或SOCKS5 URL")
		}
	}
	if c.Proxy.Mode != "direct" && c.Proxy.URL == "" {
		return fmt.Errorf("启用代理前必须配置代理地址")
	}
	for id, pol := range c.Platforms {
		if _, ok := PlatformByID(id); !ok {
			return fmt.Errorf("未知平台：%s", id)
		}
		if pol.TemplateMode != "" && pol.TemplateMode != "inherit" && pol.TemplateMode != "custom" && pol.TemplateMode != "none" {
			return fmt.Errorf("平台 %s 模板模式无效", id)
		}
		if len(pol.MessageTemplate) > 4096 {
			return fmt.Errorf("平台 %s 消息模板不能超过4096字符", id)
		}
		if pol.TemplateMode == "custom" && strings.TrimSpace(pol.MessageTemplate) == "" {
			return fmt.Errorf("平台 %s 启用自定义模板时模板内容不能为空", id)
		}
		if pol.CoverMode != "" && pol.CoverMode != "inherit" && pol.CoverMode != "on" && pol.CoverMode != "off" {
			return fmt.Errorf("平台 %s 封面模式无效", id)
		}
	}
	for _, p := range Platforms {
		if _, ok := c.Platforms[p.ID]; !ok {
			return fmt.Errorf("缺少平台配置：%s", p.ID)
		}
	}
	return nil
}

// Extraction preserves the trusted extractor context required to download a result.
type Extraction struct {
	Engine   string          `json:"engine"`
	InfoJSON json.RawMessage `json:"info_json"`
}
type FallbackAttempt struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

type Media struct {
	Kind         string            `json:"kind"`
	URL          string            `json:"url"`
	AudioURL     string            `json:"audio_url,omitempty"`
	VideoURL     string            `json:"video_url,omitempty"`
	ThumbnailURL string            `json:"thumbnail_url,omitempty"`
	BackupURLs   []string          `json:"backup_urls,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Width        int               `json:"width,omitempty"`
	Height       int               `json:"height,omitempty"`
	Duration     float64           `json:"duration,omitempty"`
	Ext          string            `json:"ext,omitempty"`
}
type ParseResult struct {
	Platform        string            `json:"platform"`
	SourceURL       string            `json:"source_url"`
	CanonicalURL    string            `json:"canonical_url,omitempty"`
	CoverURL        string            `json:"cover_url,omitempty"`
	Title           string            `json:"title"`
	Author          string            `json:"author,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	Content         string            `json:"content"`
	MarkdownContent string            `json:"markdown_content,omitempty"`
	CookieExpired   bool              `json:"cookie_expired,omitempty"`
	DegradedReason  string            `json:"degraded_reason,omitempty"`
	FallbackErrors  []FallbackAttempt `json:"fallback_errors,omitempty"`
	Extraction      *Extraction       `json:"extraction,omitempty"`
	Type            string            `json:"type"`
	Media           []Media           `json:"media"`
	Headers         map[string]string `json:"headers,omitempty"`
}
type Artifact struct {
	Kind      string  `json:"kind"`
	Path      string  `json:"path"`
	ThumbPath string  `json:"thumb_path,omitempty"`
	VideoPath string  `json:"video_path,omitempty"`
	VideoSize int64   `json:"video_size,omitempty"`
	Size      int64   `json:"size"`
	Duration  float64 `json:"duration,omitempty"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
}
type CacheEntry struct {
	Key       string      `json:"key"`
	Platform  string      `json:"platform"`
	URL       string      `json:"url"`
	Result    ParseResult `json:"result"`
	Files     []Artifact  `json:"files"`
	CreatedAt time.Time   `json:"created_at"`
	ExpiresAt time.Time   `json:"expires_at"`
}
type Task struct {
	ID        string    `json:"id"`
	BotID     string    `json:"bot_id"`
	GroupID   int64     `json:"group_id"`
	UserID    int64     `json:"user_id"`
	Platform  string    `json:"platform"`
	URL       string    `json:"url"`
	CacheKey  string    `json:"cache_key"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}
type ForwardNode struct {
	UserID   string    `json:"user_id"`
	Nickname string    `json:"nickname"`
	Content  []Segment `json:"content"`
}
type CookieAlert struct {
	Platform   string    `json:"platform"`
	Reason     string    `json:"reason"`
	DetectedAt time.Time `json:"detected_at"`
}
type Target struct {
	BotID     string `json:"bot_id"`
	GroupID   int64  `json:"group_id"`
	UserID    int64  `json:"user_id"`
	MessageID string `json:"message_id,omitempty"`
}
type Event struct {
	Target
	Segments []Segment `json:"segments"`
}
type BotStatus struct {
	SelfID      string    `json:"self_id"`
	ConnectedAt time.Time `json:"connected_at"`
	LastSeen    time.Time `json:"last_seen"`
}
