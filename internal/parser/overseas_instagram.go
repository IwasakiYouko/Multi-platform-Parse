package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"multiparse/internal/model"
)

func init() { Register("instagram", overseasInstagram); Register("threads", overseasThreads) }

var overseasLSDPattern = regexp.MustCompile(`\["LSD",\[\],\{"token":"([^"]+)"`)

func overseasPostJSON(ctx context.Context, c *http.Client, endpoint string, form url.Values, headers map[string]string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("native GraphQL returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var state any
	err = json.Unmarshal(body, &state)
	return state, err
}
func overseasInstagramPK(code string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	if len(code) > 28 {
		code = code[:len(code)-28]
	}
	value := new(big.Int)
	for _, ch := range code {
		digit := strings.IndexRune(alphabet, ch)
		if digit < 0 {
			return ""
		}
		value.Lsh(value, 6)
		value.Add(value, big.NewInt(int64(digit)))
	}
	return value.String()
}
func overseasInstagramProduct(states []any, code string) map[string]any {
	var found map[string]any
	for _, state := range states {
		overseasWalk(state, func(m map[string]any) {
			if found != nil {
				return
			}
			if product := Obj(Path(m, "xig_polaris_media", "if_not_gated_logged_out")); product != nil {
				found = product
				return
			}
			if Text(m["shortcode"]) == code || Text(m["code"]) == code {
				if m["video_versions"] != nil || m["image_versions2"] != nil || m["display_url"] != nil || m["edge_sidecar_to_children"] != nil || m["carousel_media"] != nil {
					found = m
				}
			}
		})
	}
	return found
}
func overseasInstagramMedia(r *model.ParseResult, node map[string]any) error {
	children := Array(node["carousel_media"])
	if len(children) == 0 {
		for _, edge := range Array(Path(node, "edge_sidecar_to_children", "edges")) {
			if child := Obj(Path(edge, "node")); child != nil {
				children = append(children, child)
			}
		}
	}
	if len(children) > 0 {
		for _, child := range children {
			if err := overseasInstagramMedia(r, Obj(child)); err != nil {
				return err
			}
		}
		return nil
	}
	video := overseasURL(node["video_url"])
	best := overseasBest(node["video_versions"], "url")
	if best != nil {
		video = overseasURL(best["url"])
	}
	width := int(Number(node["original_width"]))
	height := int(Number(node["original_height"]))
	if width == 0 {
		width = int(Number(Path(node, "dimensions", "width")))
		height = int(Number(Path(node, "dimensions", "height")))
	}
	image := overseasFirst(node["display_url"], node["display_src"])
	if candidate := overseasBest(Path(node, "image_versions2", "candidates"), "url"); candidate != nil {
		image = Text(candidate["url"])
		if width == 0 {
			width = int(Number(candidate["width"]))
			height = int(Number(candidate["height"]))
		}
	}
	if video != "" {
		overseasAdd(r, model.Media{Kind: "video", URL: video, ThumbnailURL: image, Width: width, Height: height, Duration: Number(node["video_duration"]), Ext: "mp4"})
		return nil
	}
	if node["is_video"] == true || Number(node["media_type"]) == 2 || Text(node["__typename"]) == "GraphVideo" {
		return fmt.Errorf("%s: video stream withheld; login, age restriction, or DRM prevents access", r.Platform)
	}
	if image != "" {
		overseasAdd(r, model.Media{Kind: "image", URL: image, Width: width, Height: height})
	}
	return nil
}
func overseasInstagramCaption(node map[string]any) string {
	caption := overseasFirst(Path(node, "caption", "text"), Path(node, "text_post_app_info", "text_fragments", "text"))
	if caption == "" {
		for _, edge := range Array(Path(node, "edge_media_to_caption", "edges")) {
			if s := Text(Path(edge, "node", "text")); s != "" {
				caption = s
				break
			}
		}
	}
	return caption
}
func overseasInstagram(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	code := overseasPathID(raw, "p", "reel", "reels", "tv")
	if code == "" {
		return nil, fmt.Errorf("instagram: provide a post, reel, or carousel URL; profiles and stories are not public posts")
	}
	headers := Headers(cookie, "https://www.instagram.com/")
	headers["X-IG-App-ID"] = "936619743392459"
	headers["X-ASBD-ID"] = "359341"
	var product map[string]any
	if cookie != "" {
		var response any
		if err := JSON(ctx, c, "https://www.instagram.com/api/v1/media/"+overseasInstagramPK(code)+"/info/", headers, &response); err == nil {
			items := Array(Path(response, "items"))
			if len(items) > 0 {
				product = Obj(items[0])
			}
		}
	}
	if product == nil {
		body, final, err := Get(ctx, c, "https://www.instagram.com/p/"+url.PathEscape(code)+"/", headers)
		if err != nil {
			return nil, fmt.Errorf("instagram: cannot access native post page: %w", err)
		}
		if strings.Contains(final, "/accounts/login") {
			return nil, fmt.Errorf("instagram: login required or anonymous access rate-limited")
		}
		product = overseasInstagramProduct(overseasStates(body), code)
		if product == nil {
			lsd := ""
			if match := overseasLSDPattern.FindSubmatch(body); len(match) > 1 {
				lsd = string(match[1])
			}
			if lsd != "" {
				headers["X-FB-LSD"] = lsd
				headers["X-FB-Friendly-Name"] = "PolarisLoggedOutDesktopWWWPostRootContentQuery"
				headers["X-Requested-With"] = "XMLHttpRequest"
				variables, _ := json.Marshal(map[string]string{"media_id": overseasInstagramPK(code)})
				state, err := overseasPostJSON(ctx, c, "https://www.instagram.com/api/graphql", url.Values{"lsd": {lsd}, "fb_api_caller_class": {"RelayModern"}, "fb_api_req_friendly_name": {"PolarisLoggedOutDesktopWWWPostRootContentQuery"}, "server_timestamps": {"true"}, "variables": {string(variables)}, "doc_id": {"27130156389949648"}}, headers)
				if err == nil {
					product = overseasInstagramProduct([]any{state}, code)
				}
			}
		}
	}
	if product == nil {
		return nil, fmt.Errorf("instagram: native post data unavailable; post may be private, deleted, login-required, or anti-bot restricted")
	}
	r := &model.ParseResult{Platform: "instagram", SourceURL: raw, Content: overseasInstagramCaption(product), Author: overseasFirst(Path(product, "user", "username"), Path(product, "owner", "username")), Title: Text(product["title"])}
	if err := overseasInstagramMedia(r, product); err != nil {
		return nil, err
	}
	if len(r.Media) == 0 {
		return nil, fmt.Errorf("instagram: native post contained no accessible photo or video")
	}
	return overseasFinish(r)
}
func overseasThreads(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	code := overseasPathID(raw, "post")
	if code == "" {
		return nil, fmt.Errorf("threads: provide an individual /@user/post/shortcode URL")
	}
	body, final, err := Get(ctx, c, raw, Headers(cookie, "https://www.threads.com/"))
	if err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	if strings.Contains(final, "/login") {
		return nil, fmt.Errorf("threads: login required")
	}
	var post map[string]any
	for _, state := range overseasStates(body) {
		overseasWalk(state, func(m map[string]any) {
			if Text(m["code"]) == code && (m["text_post_app_info"] != nil || m["caption"] != nil || m["image_versions2"] != nil || m["video_versions"] != nil) {
				post = m
			}
		})
	}
	if post == nil {
		return nil, fmt.Errorf("threads: native thread node unavailable; post may be private, removed, or require login")
	}
	r := &model.ParseResult{Platform: "threads", SourceURL: raw, Content: overseasInstagramCaption(post), Author: Text(Path(post, "user", "username"))}
	if err := overseasInstagramMedia(r, post); err != nil {
		return nil, err
	}
	if article := Text(Path(post, "text_post_app_info", "text_with_entities", "text")); article != "" {
		r.Content = article
	}
	// Attached text posts carry long text separately from the caption.
	overseasWalk(Path(post, "text_post_app_info"), func(m map[string]any) {
		if text := Text(m["plaintext"]); text != "" {
			r.Content = text
		}
	})
	return overseasFinish(r)
}
