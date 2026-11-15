package policy

import (
	"testing"

	"multiparse/internal/model"
)

func TestFormatMessageDefaultAndCustomBlocks(t *testing.T) {
	cfg := model.DefaultConfig()
	res := model.ParseResult{
		Platform:  "bilibili",
		Title:     "测试视频标题",
		Author:    "测试UP主",
		Content:   "视频简介内容",
		Type:      "video",
		SourceURL: "https://www.bilibili.com/video/BV1GJ411x7h7?p=2&vd_source=abcdef&spm_id_from=333.1007",
		Media:     []model.Media{{Kind: "video"}},
	}

	got := FormatMessage(cfg, res, nil)
	want := "【测试视频标题】\n作者：测试UP主\n视频简介内容\n链接：https://www.bilibili.com/video/BV1GJ411x7h7?p=2"
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
	pol.MessageTemplate = "{platform} {type}\n📌 标题：{raw_title}\n👤 作者：{raw_author}\n📊 {media_stats}\n🔗 {raw_clean_url}"
	cfg.Platforms["bilibili"] = pol
	got = FormatMessage(cfg, resNoAuthor, nil)
	want = "[哔哩哔哩] [视频]\n📌 标题：测试视频标题\n📊 共 1 个视频\n🔗 https://www.bilibili.com/video/BV1GJ411x7h7?p=2"
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

func TestFormatMessageDeduplicatesIdenticalTitleAndContent(t *testing.T) {
	cfg := model.DefaultConfig()
	res := model.ParseResult{
		Platform:  "douyin",
		Title:     "这是一条标题与正文完全一致的抖音作品",
		Author:    "创作者",
		Content:   "这是一条标题与正文完全一致的抖音作品",
		Type:      "video",
		SourceURL: "https://www.douyin.com/video/7706164194719099643?utm_source=copy",
	}

	got := FormatMessage(cfg, res, nil)
	want := "【这是一条标题与正文完全一致的抖音作品】\n作者：创作者\n链接：https://www.douyin.com/video/7706164194719099643"
	if got != want {
		t.Fatalf("dedup mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// If template only asks for {content} without {title}, content must NOT be dropped.
	pol := cfg.Platforms["douyin"]
	pol.TemplateMode = "custom"
	pol.MessageTemplate = "{content}"
	cfg.Platforms["douyin"] = pol
	if got = FormatMessage(cfg, res, nil); got != res.Content {
		t.Fatalf("content-only template dropped content: %q", got)
	}
}

func TestCleanTrackingURLPreservesFunctionalParams(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			"https://www.bilibili.com/video/BV1GJ411x7h7?p=3&vd_source=123&share_source=copy_web",
			"https://www.bilibili.com/video/BV1GJ411x7h7?p=3",
		},
		{
			"https://www.xiaohongshu.com/explore/66112233?xsec_token=ABC123_&xsec_source=pc_feed&xhsshare=CopyLink&appuid=99",
			"https://www.xiaohongshu.com/explore/66112233?xsec_source=pc_feed&xsec_token=ABC123_",
		},
		{
			"https://x.com/example/status/1234567890?s=20&t=xyz987",
			"https://x.com/example/status/1234567890",
		},
		{
			"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s&si=track123&feature=shared",
			"https://www.youtube.com/watch?t=42s&v=dQw4w9WgXcQ",
		},
		{
			"https://mp.weixin.qq.com/s?__biz=MzA1&mid=2650&idx=1&sn=deadbeef&utm_source=wechat",
			"https://mp.weixin.qq.com/s?__biz=MzA1&idx=1&mid=2650&sn=deadbeef",
		},
	}
	for _, tc := range cases {
		if got := CleanTrackingURL(tc.in); got != tc.want {
			t.Errorf("CleanTrackingURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
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
