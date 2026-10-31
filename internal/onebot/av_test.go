package onebot

import (
	"multiparse/internal/model"
	"reflect"
	"testing"
)

func TestExtractLegacyAVMessages(t *testing.T) {
	event := model.Event{Segments: []model.Segment{
		{Type: "text", Data: map[string]any{"text": "古早视频 AV170001?p=2"}},
		{Type: "json", Data: map[string]any{"data": `{"meta":{"news":{"jumpUrl":"https://www.bilibili.com/video/av170002?p=3"}}}`}},
		{Type: "xml", Data: map[string]any{"data": `<msg><item url="https://www.bilibili.com/video/av170003?p=4"/></msg>`}},
	}}
	want := []string{"https://www.bilibili.com/video/av170001?p=2", "https://www.bilibili.com/video/av170002?p=3", "https://www.bilibili.com/video/av170003?p=4"}
	if got := Extract(event); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy messages=%#v want%#v", got, want)
	}
	invalid := model.Event{Segments: []model.Segment{{Type: "text", Data: map[string]any{"text": "avatar av0 fav170001 av170001extra av9223372036854775808"}}}}
	if got := Extract(invalid); len(got) != 0 {
		t.Fatalf("ordinary text treated as AV: %#v", got)
	}
}
