package policy

import (
	"fmt"
	"strings"

	"multiparse/internal/model"
)

// Check returns a human-readable denial and whether the caller should stay silent.
func Check(c model.Config, t model.Target, platform string) (string, bool) {
	if !c.Policy.Enabled {
		return "自动解析已关闭", true
	}
	if t.GroupID != 0 {
		if len(c.Policy.GroupWhitelist) > 0 {
			if !contains(c.Policy.GroupWhitelist, t.GroupID) {
				return "当前群不在白名单", true
			}
		} else if contains(c.Policy.GroupBlacklist, t.GroupID) {
			return "当前群已被禁用", true
		}
	} else if t.UserID != 0 && !c.Policy.PrivateEnabled {
		return "私聊解析已关闭", true
	}
	p, ok := c.Platforms[platform]
	if !ok {
		return "不支持的平台", true
	}
	if !p.Enabled {
		return "管理员已关闭该平台解析", p.Silent
	}
	if p.NSFWBlocked {
		return "该平台因NSFW内容风险被拒绝", p.Silent
	}
	return "", false
}
func contains(a []int64, n int64) bool {
	for _, x := range a {
		if x == n {
			return true
		}
	}
	return false
}
func Message(c model.Config, platform, reason string) string {
	text := c.Policy.RejectText
	if p, ok := c.Platforms[platform]; ok && p.RejectText != "" {
		text = p.RejectText
	}
	if text == "" {
		text = "{platform} 暂不支持解析：{reason}"
	}
	name := platform
	if p, ok := model.PlatformByID(platform); ok {
		name = p.Name
	}
	return strings.NewReplacer("{platform}", name, "{reason}", reason).Replace(text)
}
func MediaCheck(c model.Config, r model.ParseResult, files []model.Artifact) error {
	images := 0
	for _, m := range r.Media {
		if m.Kind == "image" {
			images++
		}
		if m.Duration > float64(c.Media.MaxDurationSeconds) {
			return fmt.Errorf("视频超过允许时长%d秒", c.Media.MaxDurationSeconds)
		}
	}
	if images > c.Media.MaxImages {
		return fmt.Errorf("图片数量超过%d张", c.Media.MaxImages)
	}
	for _, f := range files {
		if f.Duration > float64(c.Media.MaxDurationSeconds) {
			return fmt.Errorf("缓存视频超过允许时长")
		}
		if f.Size > c.Media.MaxBytes && c.Media.OversizeMode != "file" {
			return fmt.Errorf("媒体文件超过发送大小限制")
		}
	}
	return nil
}
