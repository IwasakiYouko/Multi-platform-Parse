package parser

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"multiparse/internal/model"
)

func init() { Register("tiktok", overseasTikTok); Register("snapchat", overseasSnapchat) }

func overseasAddress(v any) string {
	if address := overseasURL(v); address != "" {
		return address
	}
	for _, item := range Array(v) {
		if address := overseasAddress(item); address != "" {
			return address
		}
	}
	node := Obj(v)
	for _, key := range []string{"urlList", "UrlList", "url_list", "src", "url", "download", "playAddr", "PlayAddr"} {
		if child := node[key]; child != nil {
			if address := overseasAddress(child); address != "" {
				return address
			}
		}
	}
	return ""
}
func overseasTikTok(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	body, final, err := Get(ctx, c, raw, Headers(cookie, "https://www.tiktok.com/"))
	if err != nil {
		return nil, fmt.Errorf("tiktok: %w", err)
	}
	id := overseasPathID(final, "video", "photo", "embed")
	if id == "" {
		return nil, fmt.Errorf("tiktok: provide a video or photo post URL, not a profile, live stream, or expired share")
	}
	var item map[string]any
	var status float64
	for _, state := range overseasStates(body) {
		overseasWalk(state, func(m map[string]any) {
			if detail := Obj(m["webapp.video-detail"]); detail != nil {
				status = Number(detail["statusCode"])
			}
			if Text(m["id"]) == id || Text(m["aweme_id"]) == id {
				if m["video"] != nil || m["imagePost"] != nil || m["image_post_info"] != nil {
					item = m
				}
			}
			if module := Obj(m["ItemModule"]); module != nil {
				if target := Obj(module[id]); target != nil {
					item = target
				}
			}
		})
	}
	if item == nil {
		switch int(status) {
		case 10216, 10222:
			return nil, fmt.Errorf("tiktok: private post or account; an authorized login is required")
		case 10204:
			return nil, fmt.Errorf("tiktok: source IP is blocked by TikTok")
		}
		return nil, fmt.Errorf("tiktok: native post state unavailable (status %d); post may be removed, region-restricted, login-required, or blocked by anti-bot checks", int(status))
	}
	r := &model.ParseResult{Platform: "tiktok", SourceURL: final, Title: Text(item["desc"]), Content: Text(item["desc"]), Author: overseasFirst(Path(item, "author", "nickname"), Path(item, "author", "uniqueId"), Path(item, "authorInfo", "nickname"))}
	photos := Array(Path(item, "imagePost", "images"))
	if len(photos) == 0 {
		photos = Array(Path(item, "image_post_info", "images"))
	}
	for _, photo := range photos {
		address := overseasAddress(Path(photo, "imageURL"))
		if address == "" {
			address = overseasAddress(Path(photo, "display_image"))
		}
		if address == "" {
			address = overseasAddress(Path(photo, "image_url"))
		}
		if address == "" {
			return nil, fmt.Errorf("tiktok: slideshow contains a photo without an accessible native image URL")
		}
		overseasAdd(r, model.Media{Kind: "image", URL: address, Width: int(Number(Path(photo, "imageWidth"))), Height: int(Number(Path(photo, "imageHeight")))})
	}
	if len(photos) > 0 {
		return overseasFinish(r)
	}
	video := Obj(item["video"])
	if video["isDRM"] == true || video["is_drm"] == true || video["is_encrypted"] == true {
		return nil, fmt.Errorf("tiktok: DRM-encrypted video is unsupported")
	}
	address := overseasAddress(video["playAddr"])
	if address == "" {
		address = overseasAddress(video["play_addr"])
	}
	bestRate := float64(-1)
	for _, entry := range Array(video["bitrateInfo"]) {
		key := strings.ToLower(Text(Path(entry, "PlayAddr", "UrlKey")))
		if strings.Contains(key, "bytevc2") || strings.Contains(key, "bytevc1") || strings.Contains(key, "hvc1") {
			continue
		}
		candidate := overseasAddress(Path(entry, "PlayAddr"))
		rate := Number(Path(entry, "Bitrate"))
		if candidate != "" && rate > bestRate {
			address = candidate
			bestRate = rate
		}
	}
	if address == "" {
		address = overseasAddress(video["downloadAddr"])
	}
	if address == "" {
		address = overseasAddress(video["download_addr"])
	}
	if address == "" {
		return nil, fmt.Errorf("tiktok: post contains no accessible native video stream; login, availability, or signature checks failed")
	}
	duration := Number(video["duration"])
	if video["play_addr"] != nil {
		duration /= 1000
	}
	overseasAdd(r, model.Media{Kind: "video", URL: address, ThumbnailURL: overseasAddress(video["cover"]), Width: int(Number(video["width"])), Height: int(Number(video["height"])), Duration: duration, Ext: "mp4"})
	return overseasFinish(r)
}
func overseasSnapchat(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	body, final, err := Get(ctx, c, raw, Headers(cookie, "https://www.snapchat.com/"))
	if err != nil {
		return nil, fmt.Errorf("snapchat: %w", err)
	}
	id := overseasPathID(final, "spotlight")
	if id == "" {
		return nil, fmt.Errorf("snapchat: only public Spotlight post URLs expose downloadable native video; provide /spotlight/ID")
	}
	var metadata map[string]any
	for _, state := range overseasStates(body) {
		overseasWalk(state, func(m map[string]any) {
			if Text(Path(m, "story", "storyId", "value")) == id {
				metadata = Obj(m["metadata"])
			}
		})
	}
	if metadata == nil {
		return nil, fmt.Errorf("snapchat: Spotlight native story state unavailable; post may be expired, private, removed, or region-restricted")
	}
	video := Obj(metadata["videoMetadata"])
	address := overseasURL(video["contentUrl"])
	if address == "" {
		return nil, fmt.Errorf("snapchat: Spotlight story has no accessible video content URL")
	}
	r := &model.ParseResult{Platform: "snapchat", SourceURL: final, Title: Text(video["name"]), Content: overseasFirst(metadata["description"], video["description"]), Author: Text(Path(video, "creator", "personCreator", "username"))}
	overseasAdd(r, model.Media{Kind: "video", URL: address, ThumbnailURL: Text(video["thumbnailUrl"]), Width: int(Number(video["width"])), Height: int(Number(video["height"])), Duration: Number(video["durationMs"]) / 1000, Ext: "mp4"})
	return overseasFinish(r)
}
