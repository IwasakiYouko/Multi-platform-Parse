package onebot

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"multiparse/internal/model"
)

func TestExtractRichMessages(t *testing.T) {
	card, err := json.Marshal(map[string]any{
		"meta": map[string]any{"detail_1": map[string]any{
			"qqdocurl": "https://b23.tv/abc?first=1&second=2",
			"mixed":    []any{map[string]any{"jumpUrl": "https://v.douyin.com/def/"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	escapedCard, err := json.Marshal(string(card))
	if err != nil {
		t.Fatal(err)
	}
	event := model.Event{Segments: []model.Segment{
		{Type: "text", Data: map[string]any{"text": "看这个 BV1xx411c7mD，以及 https://www.bilibili.com/video/BV1xx411c7mD。"}},
		{Type: "json", Data: map[string]any{"data": string(escapedCard)}},
		{Type: "xml", Data: map[string]any{"data": `<msg><item url="https://mp.weixin.qq.com/s/ghi?a=1&amp;b=2"><title><![CDATA[https://www.zhihu.com/question/123]]></title></item></msg>`}},
		{Type: "share", Data: map[string]any{"url": "https://xhslink.com/jkl", "content": map[string]any{"nested": `https:\/\/www.youtube.com\/watch?v=123\u0026t=1`}}},
		{Type: "image", Data: map[string]any{"url": "https://www.douban.com/photos/ignored"}},
	}}
	want := []string{
		"https://www.bilibili.com/video/BV1xx411c7mD",
		"https://v.douyin.com/def/",
		"https://b23.tv/abc?first=1&second=2",
		"https://mp.weixin.qq.com/s/ghi?a=1&b=2",
		"https://www.zhihu.com/question/123",
		"https://www.youtube.com/watch?v=123&t=1",
		"https://xhslink.com/jkl",
	}
	if got := Extract(event); !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract() = %#v, want %#v", got, want)
	}
}

func TestExtractOnlyRecognizedPlatforms(t *testing.T) {
	event := model.Event{Segments: []model.Segment{{Type: "text", Data: map[string]any{"text": strings.Join([]string{
		"https://example.com/post",
		"https://b23.tv.evil.example/x",
		"https://b23.tv@evil.example/x",
		"https://evil.example/b23.tv/x",
		"https://b23.tv:9999/x",
		"https://evil.example/BV1xx411c7mD",
		"javascript:alert(1)",
		"b23.tv.evil.example/x",
		"https://b23.tv/valid",
	}, " ")}}}}
	want := []string{"https://b23.tv/valid"}
	if got := Extract(event); !reflect.DeepEqual(got, want) {
		t.Fatalf("unrecognized URL escaped filtering: %#v", got)
	}
}

func TestExtractCQAndEscapedJSON(t *testing.T) {
	event := model.Event{Segments: []model.Segment{{Type: "text", Data: map[string]any{
		"text": `[CQ:json,data={&quot;meta&quot;:{&quot;news&quot;:{&quot;jumpUrl&quot;:&quot;https:\/\/b23.tv\/cq?x=1&amp;y=2&quot;}}}]`,
	}}}}
	if got := Extract(event); !reflect.DeepEqual(got, []string{"https://b23.tv/cq?x=1&y=2"}) {
		t.Fatalf("CQ extraction = %#v", got)
	}
}

func TestExtractCQShareFieldEscapes(t *testing.T) {
	event := model.Event{Segments: []model.Segment{{Type: "text", Data: map[string]any{
		"text": `[CQ:share,url=https://b23.tv/share?items=1&#44;2&amp;x=3,title=分享,content=视频]`,
	}}}}
	if got := Extract(event); !reflect.DeepEqual(got, []string{"https://b23.tv/share?items=1,2&x=3"}) {
		t.Fatalf("CQ share field delimiters changed its URL: %#v", got)
	}
}

func TestExtractXMLDoesNotResolveEntities(t *testing.T) {
	event := model.Event{Segments: []model.Segment{{Type: "xml", Data: map[string]any{
		"data": `<!DOCTYPE msg [<!ENTITY remote SYSTEM "https://b23.tv/must-not-fetch">]><msg>&remote;</msg>`,
	}}}}
	if got := Extract(event); len(got) != 0 {
		t.Fatalf("entity directive treated as a share link: %#v", got)
	}
}

func TestExtractPreservesSignedQueryAndAngleLinks(t *testing.T) {
	link := "https://b23.tv/signed?copy=1&reg=2&not=3&signature=a%2Fb%2Bc"
	event := model.Event{Segments: []model.Segment{{Type: "text", Data: map[string]any{"text": "<" + link + ">"}}}}
	if got := Extract(event); !reflect.DeepEqual(got, []string{link}) {
		t.Fatalf("plain URL was changed while decoding card escapes: %#v", got)
	}
}

func TestExtractBounds(t *testing.T) {
	var text strings.Builder
	for i := range 100 {
		fmt.Fprintf(&text, "https://b23.tv/%d ", i)
	}
	event := model.Event{Segments: []model.Segment{{Type: "text", Data: map[string]any{"text": text.String()}}}}
	if got := Extract(event); len(got) != maxExtractLinks {
		t.Fatalf("link limit: got %d", len(got))
	}
	var nested any = "https://b23.tv/too-deep"
	for range maxExtractDepth + 2 {
		nested = map[string]any{"nested": nested}
	}
	event.Segments = []model.Segment{{Type: "json", Data: map[string]any{"data": nested}}}
	if got := Extract(event); len(got) != 0 {
		t.Fatalf("nesting limit not applied: %#v", got)
	}
}
