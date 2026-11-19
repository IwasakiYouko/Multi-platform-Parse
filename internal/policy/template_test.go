package policy

import (
	"strings"
	"testing"

	"multiparse/internal/model"
)

func TestFormatMessageDefaultAndCustomBlocks(t *testing.T) {
	cfg := model.DefaultConfig()
	res := model.ParseResult{
		Platform:  "bilibili",
		Title:     "测试视频标题",
		Author:    "测试UP主",
		Tags:      []string{"科技", "数码"},
		Content:   "视频简介内容 #科技 #测评",
		Type:      "video",
		SourceURL: "https://www.bilibili.com/video/BV1GJ411x7h7?p=2&vd_source=abcdef&spm_id_from=333.1007",
		Media:     []model.Media{{Kind: "video"}},
	}

	got := FormatMessage(cfg, res, nil)
	want := "【测试视频标题】\n#科技 #数码 #测评\n视频简介内容\n链接：https://www.bilibili.com/video/BV1GJ411x7h7?p=2"
	if got != want {
		t.Fatalf("default template mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// Title-only override on a single platform.
	pol := cfg.Platforms["bilibili"]
	pol.TemplateMode = "custom"
	pol.MessageTemplate = "{title}"
	cfg.Platforms["bilibili"] = pol
	if got = FormatMessage(cfg, res, nil); got != "【测试视频标题】" {
		t.Fatalf("title-only template = %q, want %q", got, "【测试视频标题】")
	}

	// Smart line collapsing when Author is empty even with static prefix on the same line.
	resNoAuthor := res
	resNoAuthor.Author = ""
	pol.MessageTemplate = "{platform} {type}\n📌 标题：{raw_title}\n👤 作者：{raw_author}\n🏷️ 标签：{raw_tags}\n📊 {media_stats}\n🔗 {raw_clean_url}"
	cfg.Platforms["bilibili"] = pol
	got = FormatMessage(cfg, resNoAuthor, nil)
	want = "[哔哩哔哩] [视频]\n📌 标题：测试视频标题\n🏷️ 标签：科技 · 数码 · 测评\n📊 共 1 个视频\n🔗 https://www.bilibili.com/video/BV1GJ411x7h7?p=2"
	if got != want {
		t.Fatalf("smart collapse mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// TemplateMode = none suppresses text block.
	pol.TemplateMode = "none"
	cfg.Platforms["bilibili"] = pol
	if got = FormatMessage(cfg, res, nil); got != "" {
		t.Fatalf("none mode = %q, want empty", got)
	}
}

func TestFormatMessageDeduplicatesIdenticalTitleAndContentAndSummary(t *testing.T) {
	cfg := model.DefaultConfig()
	res := model.ParseResult{
		Platform:     "douyin",
		Title:        "这是一条标题与正文完全一致的抖音作品 #日常 #搞笑",
		Author:       "创作者",
		Content:      "这是一条标题与正文完全一致的抖音作品 #日常 #搞笑",
		Type:         "video",
		SourceURL:    "https://v.douyin.com/wfzA1peuTCM/",
		CanonicalURL: "https://www.iesdouyin.com/share/video/7706164194719099643/?region=CN&mid=123&u_code=abc",
	}

	got := FormatMessage(cfg, res, nil)
	want := "【这是一条标题与正文完全一致的抖音作品】\n#日常 #搞笑\n链接：https://www.douyin.com/video/7706164194719099643"
	if got != want {
		t.Fatalf("dedup mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// If template only asks for {content} without {title} or {tags}, content must NOT be dropped.
	pol := cfg.Platforms["douyin"]
	pol.TemplateMode = "custom"
	pol.MessageTemplate = "{content}"
	cfg.Platforms["douyin"] = pol
	if got = FormatMessage(cfg, res, nil); got != res.Content {
		t.Fatalf("content-only template dropped content: %q", got)
	}

	// SummaryLength truncates Message 1 and exposes FullTextForForward.
	cfg = model.DefaultConfig()
	cfg.Media.SummaryLength = 20
	longRes := model.ParseResult{
		Platform:  "xhs",
		Title:     "长文标题",
		Content:   strings.Repeat("正文内容测试", 10) + "\n#穿搭[话题]# #日常[话题]#",
		SourceURL: "https://www.xiaohongshu.com/explore/66112233",
	}
	rendered := FormatMessage(cfg, longRes, nil)
	if !strings.Contains(rendered, "#穿搭 #日常") || strings.Contains(rendered, "[话题]") {
		t.Fatalf("tags not extracted cleanly: %q", rendered)
	}
	full, truncated := FullTextForForward(cfg, longRes)
	if !truncated || full != strings.Repeat("正文内容测试", 10) {
		t.Fatalf("FullTextForForward = (%q, %v)", full, truncated)
	}

	// FullTextForForward must return false when template omits {content} / {raw_content}.
	xhsPol := cfg.Platforms["xhs"]
	xhsPol.TemplateMode = "custom"
	xhsPol.MessageTemplate = "{title}\n{url}"
	cfg.Platforms["xhs"] = xhsPol
	if full, truncated = FullTextForForward(cfg, longRes); truncated || full != "" {
		t.Fatalf("expected FullTextForForward disabled for title-only template, got (%q, %v)", full, truncated)
	}

	// Trailing URL fragment like https://example.com/#chapter must not be stripped as a hashtag.
	tags, cleaned := ExtractAndCleanTags(nil, "详情见 https://example.com/docs/#chapter")
	if len(tags) != 0 || cleaned != "详情见 https://example.com/docs/#chapter" {
		t.Fatalf("URL fragment misparsed as hashtag: tags=%v cleaned=%q", tags, cleaned)
	}
}

func TestCleanTrackingURLStrictAllowlistsAcrossPlatforms(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			"https://www.bilibili.com/video/BV1GJ411x7h7/?p=1&vd_source=123&share_source=copy_web&unique_k=999",
			"https://www.bilibili.com/video/BV1GJ411x7h7",
		},
		{
			"https://m.bilibili.com/video/BV1GJ411x7h7?p=3&vd_source=123&bbid=XYZ",
			"https://www.bilibili.com/video/BV1GJ411x7h7?p=3",
		},
		{
			"https://www.xiaohongshu.com/discovery/item/66112233?source=webshare&xhsshare=pc_web&appuid=99&apptime=170000&author_share=1&xsec_token=ABC123_&xsec_source=pc_share",
			"https://www.xiaohongshu.com/explore/66112233?xsec_source=pc_share&xsec_token=ABC123_",
		},
		{
			"https://www.iesdouyin.com/share/video/7706164194719099643/?region=CN&mid=11&u_code=aa&with_sec_did=1&share_sign=zz",
			"https://www.douyin.com/video/7706164194719099643",
		},
		{
			"https://v.m.chenzhongtech.com/fw/photo/3xabcdef?fid=12345&cc=share_copylink&shareToken=X&kpn=KUAISHOU",
			"https://www.kuaishou.com/short-video/3xabcdef",
		},
		{
			"https://twitter.com/example/status/1234567890?s=20&t=xyz987",
			"https://x.com/example/status/1234567890",
		},
		{
			"https://www.instagram.com/reel/C12345/?igsh=MzRlODBiNWFlZA==&img_index=1",
			"https://www.instagram.com/reel/C12345/",
		},
		{
			"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s&si=track123&feature=shared",
			"https://www.youtube.com/watch?t=42s&v=dQw4w9WgXcQ",
		},
		{
			"https://mp.weixin.qq.com/s?__biz=MzA1&mid=2650&idx=1&sn=deadbeef&chksm=aaa&mpshare=1&scene=1&utm_source=wechat",
			"https://mp.weixin.qq.com/s?__biz=MzA1&idx=1&mid=2650&sn=deadbeef",
		},
		{
			"https://example.com/#/post/42",
			"https://example.com/#/post/42",
		},
	}
	for _, tc := range cases {
		if got := CleanTrackingURL(tc.in); got != tc.want {
			t.Errorf("CleanTrackingURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	xhsA := "https://www.xiaohongshu.com/explore/66112233?xsec_token=TOKEN_A&xsec_source=pc_share&xhsshare=CopyLink"
	xhsB := "https://www.xiaohongshu.com/discovery/item/66112233?xsec_token=TOKEN_B&xsec_source=app_share"
	if idA, idB := WorkIdentityURL(xhsA), WorkIdentityURL(xhsB); idA != "https://www.xiaohongshu.com/explore/66112233" || idA != idB {
		t.Fatalf("WorkIdentityURL failed to unify XHS tokens: idA=%q idB=%q", idA, idB)
	}
}

func TestEffectiveShowCover(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.Media.ShowCover = true
	if !EffectiveShowCover(cfg, "bilibili") {
		t.Fatal("expected inherit true")
	}
	pol := cfg.Platforms["bilibili"]
	pol.CoverMode = "off"
	cfg.Platforms["bilibili"] = pol
	if EffectiveShowCover(cfg, "bilibili") {
		t.Fatal("expected per-platform off")
	}
	cfg.Media.ShowCover = false
	pol.CoverMode = "on"
	cfg.Platforms["bilibili"] = pol
	if !EffectiveShowCover(cfg, "bilibili") {
		t.Fatal("expected per-platform on")
	}
}
