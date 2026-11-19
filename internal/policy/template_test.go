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
			"https://www.xiaohongshu.com/explore/66112233",
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

func TestAll24PlatformsCanonicalCleanURLs(t *testing.T) {
	matrix := map[string][][2]string{
		"bilibili": {
			{"https://t.bilibili.com/987654321098765432?spm_id_from=333.999", "https://www.bilibili.com/opus/987654321098765432"},
			{"https://www.bilibili.com/video/BV1GJ411x7h7?p=1&t=0&start_progress=1500&vd_source=abc", "https://www.bilibili.com/video/BV1GJ411x7h7"},
			{"https://www.bilibili.com/read/mobile/123456?from=search", "https://www.bilibili.com/read/cv123456"},
		},
		"douyin": {
			{"https://www.iesdouyin.com/share/slides/7706164194719099643/?region=CN&u_code=abc", "https://www.douyin.com/note/7706164194719099643"},
			{"https://www.douyin.com/discover?modal_id=7706164194719099643&vid=111", "https://www.douyin.com/video/7706164194719099643"},
		},
		"tiktok": {
			{"https://www.tiktok.com/@creator/video/7300000000000000001?_t=8m123&_r=1&is_from_webapp=1", "https://www.tiktok.com/@creator/video/7300000000000000001"},
		},
		"xhs": {
			{
				"https://www.xiaohongshu.com/explore/6afe4c31000000002a0287ad?xsec_source=app_share&xsec_token=CBVT-Sn_OXkM7hQjDQNx9pAElQl9mNnIK8UyX7q8Alm3w%3D",
				"https://www.xiaohongshu.com/explore/6afe4c31000000002a0287ad",
			},
			{
				"https://www.xiaohongshu.com/user/profile/5f00112233445566778899aa/6afe4c31000000002a0287ad?xhsshare=CopyLink&appuid=123",
				"https://www.xiaohongshu.com/explore/6afe4c31000000002a0287ad",
			},
		},
		"weibo": {
			{"https://video.weibo.com/show?fid=1034:4900000000000001&from=singlemessage", "https://weibo.com/tv/show/1034:4900000000000001"},
			{"https://www.weibo.com/1234567890/Oabc123?from=page_100505&mod=weibotime", "https://weibo.com/1234567890/Oabc123"},
			{"https://m.weibo.cn/detail/4912345678901234?wm=3333_2001&from=10E0093010", "https://m.weibo.cn/detail/4912345678901234"},
		},
		"kuaishou": {
			{"https://www.kuaishou.com/f/X-123abc?cc=share_copylink&fid=999", "https://www.kuaishou.com/f/X-123abc"},
		},
		"twitter": {
			{"https://fixupx.com/openai/status/1800000000000000000?s=46&t=abcdef", "https://x.com/openai/status/1800000000000000000"},
		},
		"instagram": {
			{"https://www.instagram.com/p/C9ABCDEF/?igsh=ZXYxMjM0&img_index=2", "https://www.instagram.com/p/C9ABCDEF/"},
		},
		"youtube": {
			{"https://youtu.be/dQw4w9WgXcQ?si=sharetoken123&t=30", "https://www.youtube.com/watch?t=30&v=dQw4w9WgXcQ"},
			{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=RDdQw4w9WgXcQ&start_radio=1&pp=ygUF", "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		},
		"facebook": {
			{"https://www.facebook.com/watch/?v=9876543210&mibextid=WC7FNe&rdid=abc", "https://www.facebook.com/watch/?v=9876543210"},
		},
		"threads": {
			{"https://www.threads.net/@zuck/post/C8xyz123?xmt=AQGz12345&slof=1", "https://www.threads.com/@zuck/post/C8xyz123"},
		},
		"tieba": {
			{"https://tiebap.baidu.com/p/8888888888?see_lz=0&pn=1&share=9105&fr=sharewise&sfc=copy", "https://tieba.baidu.com/p/8888888888"},
			{"https://tieba.baidu.com/p/8888888888?see_lz=1&pn=3&share=9105", "https://tieba.baidu.com/p/8888888888?pn=3&see_lz=1"},
		},
		"weixin": {
			{"https://mp.weixin.qq.com/s/AbCdEfGhIjKlMnOpQrStUv?mpshare=1&scene=1&srcid=0101&sharer_shareinfo=xyz", "https://mp.weixin.qq.com/s/AbCdEfGhIjKlMnOpQrStUv"},
		},
		"weixin_channels": {
			{"https://weixin.qq.com/sph/AwXYZ123?exportkey=A4s123&pass_ticket=999", "https://weixin.qq.com/sph/AwXYZ123"},
			{"https://channels.weixin.qq.com/finder-preview/pages/sph?id=AwXYZ999&token=secret", "https://weixin.qq.com/sph/AwXYZ999"},
		},
		"coolapk": {
			{"https://www.coolapk1s.com/feed/55667788?shareKey=abc123&shareUid=10001&shareFrom=com.coolapk.market", "https://www.coolapk.com/feed/55667788"},
		},
		"pipix": {
			{"https://h5.pipix.com/item/7399999999999999999?app_id=1319&app=super&timestamp=1700000000&user_id=888", "https://h5.pipix.com/item/7399999999999999999"},
		},
		"zuiyou": {
			{"https://h5.izuiyou.com/detail/299887766?zy_to=applink&share_count=1&m=abc", "https://share.xiaochuankeji.cn/hybrid/share/post?pid=299887766"},
			{"https://share.xiaochuankeji.cn/hybrid/share/post?pid=00299887766&zy_to=applink", "https://share.xiaochuankeji.cn/hybrid/share/post?pid=299887766"},
		},
		"xiaoheihe": {
			{"https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id=befc72d7f130", "https://www.xiaoheihe.cn/app/bbs/link/befc72d7f130"},
			{"https://www.xiaoheihe.com/app/bbs/link/befc72d7f130?heybox_id=12345&os_type=iOS", "https://www.xiaoheihe.cn/app/bbs/link/befc72d7f130"},
		},
		"snapchat": {
			{"https://www.snapchat.com/spotlight/W7_EDlXWTBiXAEEniNoMPwAAYa?share_id=xyz&locale=zh-CN", "https://www.snapchat.com/spotlight/W7_EDlXWTBiXAEEniNoMPwAAYa"},
		},
		"zhihu": {
			{"https://www.zhihu.com/appview/p/680011223?utm_psn=1800000000&utm_source=wechat_session", "https://zhuanlan.zhihu.com/p/680011223"},
			{"https://m.zhihu.com/question/12345/answer/67890?utm_psn=999&share_code=abc", "https://www.zhihu.com/question/12345/answer/67890"},
		},
		"douban": {
			{"https://www.douban.com/doubanapp/dispatch?uri=/group/topic/301234567/&dt_dapp=1", "https://www.douban.com/group/topic/301234567/"},
			{"https://m.douban.com/group/topic/301234567/?_i=1234567abc&tt_from=weixin", "https://www.douban.com/group/topic/301234567/"},
		},
		"bangumi": {
			{"https://bangumi.tv/subject/400602?utm_source=share&chii_sid=xyz", "https://bgm.tv/subject/400602"},
		},
		"toutiao": {
			{"https://m.toutiao.com/i7388888888888888888/?tt_from=copy_link&utm_campaign=client_share&timestamp=1710000", "https://www.toutiao.com/article/7388888888888888888/"},
			{"https://m.toutiao.com/w/1800000000000001/?tt_from=weixin&app=news_article", "https://www.toutiao.com/w/1800000000000001/"},
		},
		"medium": {
			{"https://medium.com/@author/deep-dive-go-123456abcdef?sk=secret123&source=friends_link", "https://medium.com/@author/deep-dive-go-123456abcdef"},
		},
	}

	if len(model.Platforms) != 24 {
		t.Fatalf("expected 24 registered platforms, got %d", len(model.Platforms))
	}
	for _, p := range model.Platforms {
		cases, ok := matrix[p.ID]
		if !ok || len(cases) == 0 {
			t.Fatalf("missing canonical URL test coverage for platform %q", p.ID)
		}
		for _, tc := range cases {
			if got := CleanTrackingURL(tc[0]); got != tc[1] {
				t.Errorf("[%s] CleanTrackingURL(%q) = %q, want %q", p.ID, tc[0], got, tc[1])
			}
		}
	}
}

func TestXHSKeepTokenOptionAndCleanURLOverride(t *testing.T) {
	cfg := model.DefaultConfig()
	res := model.ParseResult{
		Platform:     "xhs",
		Title:        "基米氖（Gemini 4 Neon）震撼发布",
		Tags:         []string{"gemini", "奶龙", "奶娃", "Gemini4Neon"},
		SourceURL:    "https://www.xiaohongshu.com/explore/6afe4c31000000002a0287ad?xsec_source=app_share&xsec_token=CBVT-Sn_OXkM7hQjDQNx9pAElQl9mNnIK8UyX7q8Alm3w%3D",
		CanonicalURL: "https://www.xiaohongshu.com/explore/6afe4c31000000002a0287ad",
	}

	// Default: 100% parameter-free pure URL.
	gotDefault := FormatMessage(cfg, res, nil)
	wantDefault := "【基米氖（Gemini 4 Neon）震撼发布】\n#gemini #奶龙 #奶娃 #Gemini4Neon\n链接：https://www.xiaohongshu.com/explore/6afe4c31000000002a0287ad"
	if gotDefault != wantDefault {
		t.Fatalf("default XHS message mismatch:\ngot:  %q\nwant: %q", gotDefault, wantDefault)
	}

	// Optional XHSKeepToken = true: attaches xsec_token ONLY, still stripping xsec_source!
	cfg.Media.XHSKeepToken = true
	gotToken := DisplayResultURL(cfg, res)
	wantToken := "https://www.xiaohongshu.com/explore/6afe4c31000000002a0287ad?xsec_token=CBVT-Sn_OXkM7hQjDQNx9pAElQl9mNnIK8UyX7q8Alm3w%3D"
	if gotToken != wantToken {
		t.Fatalf("XHSKeepToken DisplayResultURL = %q, want %q", gotToken, wantToken)
	}
	// {clean_url} must ALWAYS stay 100% zero-parameter even when XHSKeepToken is true.
	if gotClean := FormatWithTemplate(cfg, "{raw_clean_url}", res, nil); gotClean != "https://www.xiaohongshu.com/explore/6afe4c31000000002a0287ad" {
		t.Fatalf("{raw_clean_url} leaked token: %q", gotClean)
	}
}

func TestDurationSanitizeInlineURLAndForwardTextChunks(t *testing.T) {
	cfg := model.DefaultConfig()
	res := model.ParseResult{
		Platform:  "xiaoheihe",
		Title:     "疯狂星期四\u200b无门槛领满30减15券\ufeff",
		Content:   "详情请看\u200b链接 https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id=befc72d7f130 即可领取！\n\n\n\n第二段说明",
		SourceURL: "https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id=befc72d7f130",
		Media:     []model.Media{{Kind: "video", Duration: 272}},
	}
	got := FormatWithTemplate(cfg, "{title}\n{duration}\n{content}\n{url}", res, nil)
	want := "【疯狂星期四无门槛领满30减15券】\n时长：04:32\n详情请看链接 https://www.xiaoheihe.cn/app/bbs/link/befc72d7f130 即可领取！\n\n第二段说明\n链接：https://www.xiaoheihe.cn/app/bbs/link/befc72d7f130"
	if got != want {
		t.Fatalf("duration/sanitize/inline-url mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// Image post with 0 duration collapses {duration} line automatically.
	resImg := res
	resImg.Media = []model.Media{{Kind: "image"}}
	gotImg := FormatWithTemplate(cfg, "{title}\n⏱️ {raw_duration}", resImg, nil)
	if gotImg != "【疯狂星期四无门槛领满30减15券】" {
		t.Fatalf("expected zero duration line collapsed, got %q", gotImg)
	}

	// ForwardTextChunks splits long articles cleanly into <= 1500-rune paragraph nodes.
	para := strings.Repeat("这是一段深度测评长文内容。", 60) // 780 runes per paragraph
	longArticle := model.ParseResult{
		Platform:  "zhihu",
		Title:     "深度长文",
		Content:   strings.Join([]string{para, para, para, para}, "\n\n"),
		SourceURL: "https://zhuanlan.zhihu.com/p/680011223",
	}
	chunks := ForwardTextChunks(cfg, longArticle)
	if len(chunks) < 3 {
		t.Fatalf("expected >= 3 chunks for 3120-rune article, got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n == 0 || n > 1500 {
			t.Fatalf("chunk[%d] rune length = %d, want 1..1500", i, n)
		}
	}
}
