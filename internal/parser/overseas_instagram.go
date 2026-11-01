package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"multiparse/internal/model"
)

func init() { Register("instagram", overseasInstagram); Register("threads", overseasThreads) }

type instagramRestrictedError struct{ reason string }

func (e *instagramRestrictedError) Error() string { return "instagram: " + e.reason }

func overseasInstagramPK(code string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	if len(code) > 28 {
		code = code[:len(code)-28]
	}
	return overseasMetaPK(code, alphabet)
}

func overseasMetaPK(code, alphabet string) string {
	if code == "" {
		return ""
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

func overseasMetaSession(c *http.Client, platform, cookie string) *overseasSession {
	defaults := "sessionid=; ds_user_id=; csrftoken=; mid=; ig_did=; "
	if platform == "instagram" {
		defaults = "sessionid=; mid=; ig_pr=1; ig_vw=1920; csrftoken=; s_network=; ds_user_id=; "
	}
	s := newOverseasSession(c, platform+".com", defaults+cookie)
	if s.client.Timeout == 0 || s.client.Timeout > 30*time.Second {
		s.client.Timeout = 30 * time.Second
	}
	if platform == "instagram" {
		s.headers["User-Agent"] = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
		s.headers["Accept-Language"] = "en-US,en;q=0.8"
	} else {
		s.headers["X-IG-App-ID"] = "238260118697367"
	}
	return s
}

func overseasMetaShare(ctx context.Context, c *http.Client, platform, raw string) (string, error) {
	s := newOverseasSession(c, platform+".com", "")
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Port() != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%s: invalid post URL", platform)
	}
	u.Scheme = "https"
	host := strings.ToLower(u.Hostname())
	if platform == "instagram" && (host == "instagr.am" || host == "www.instagr.am") {
		u.Host = "www.instagram.com"
	}
	if platform == "threads" && (host == "threads.net" || host == "www.threads.net") {
		u.Host = "www.threads.com"
	}
	raw = u.String()
	if !s.allowed(u) {
		return "", fmt.Errorf("%s: invalid post URL", platform)
	}
	if strings.Contains(u.Path, "/share/") {
		_, final, err := s.request(ctx, http.MethodGet, raw, nil, true)
		if err != nil {
			return "", err
		}
		return final, nil
	}
	return raw, nil
}

func overseasInstagramCaption(node map[string]any) string {
	edges := Array(Path(node, "edge_media_to_caption", "edges"))
	if len(edges) > 0 {
		return Text(Path(edges[0], "node", "text"))
	}
	if caption := Obj(node["caption"]); caption != nil {
		return Text(caption["text"])
	}
	return Text(node["caption"])
}

// The web_info schema uses source order, not a best-resolution heuristic.
func overseasMetaFirst(value any) map[string]any {
	items := Array(value)
	if len(items) == 0 {
		return nil
	}
	return Obj(items[0])
}

func overseasInstagramMedia(r *model.ParseResult, node map[string]any) error {
	children := Array(node["carousel_media"])
	if len(children) == 0 {
		for _, edge := range Array(Path(node, "edge_sidecar_to_children", "edges")) {
			children = append(children, Path(edge, "node"))
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
	typename := strings.TrimPrefix(Text(node["__typename"]), "XDT")
	if typename == "GraphSidecar" || Number(node["media_type"]) == 8 {
		return fmt.Errorf("instagram: empty carousel")
	}
	image, width, height := overseasFirst(node["display_url"], node["display_src"]), int(Number(Path(node, "dimensions", "width"))), int(Number(Path(node, "dimensions", "height")))
	if first := overseasMetaFirst(Path(node, "image_versions2", "candidates")); first != nil {
		image, width, height = Text(first["url"]), int(Number(first["width"])), int(Number(first["height"]))
	}
	video := Text(node["video_url"])
	if first := overseasMetaFirst(node["video_versions"]); first != nil {
		video = Text(first["url"])
	}
	isVideo := node["is_video"] == true || typename == "GraphVideo" || Number(node["media_type"]) == 2
	if isVideo {
		if overseasURL(video) == "" {
			return fmt.Errorf("instagram: video stream missing")
		}
		overseasAdd(r, model.Media{Kind: "video", URL: video, ThumbnailURL: overseasURL(image), Width: width, Height: height, Duration: Number(node["video_duration"]), Ext: "mp4"})
		return nil
	}
	if overseasURL(image) == "" {
		return fmt.Errorf("instagram: image URL missing")
	}
	overseasAdd(r, model.Media{Kind: "image", URL: image, Width: width, Height: height})
	return nil
}

func overseasInstagramRuling(ctx context.Context, c *http.Client, code, cookie string) error {
	s := overseasMetaSession(c, "instagram", cookie)
	if err := s.csrf(ctx, true); err != nil {
		return nil
	}
	s.headers["X-IG-App-ID"] = "936619743392459"
	s.headers["Accept-Language"] = "zh-CN,zh;q=0.9"
	query := url.Values{"content_type": {"MEDIA"}, "target_id": {overseasInstagramPK(code)}}
	data, _, err := s.request(ctx, http.MethodGet, "https://www.instagram.com/api/v1/web/get_ruling_for_content/?"+query.Encode(), nil, false)
	if err != nil {
		return nil
	}
	var body map[string]any
	if json.Unmarshal(data, &body) != nil {
		return nil
	}
	if Text(body["message"]) == "Media cannot be found" {
		return &ContentUnavailableError{Reason: "instagram: post does not exist or was deleted"}
	}
	if title := Text(body["title"]); title != "" {
		if description := Text(body["description"]); description != "" {
			title += ": " + description
		}
		return &instagramRestrictedError{reason: title}
	}
	return nil
}

func overseasInstagramFetch(ctx context.Context, c *http.Client, code, cookie string) (map[string]any, error) {
	s := overseasMetaSession(c, "instagram", cookie)
	if err := s.csrf(ctx, true); err != nil {
		return nil, err
	}
	payload, err := s.graphql(ctx, "27128499623469141", map[string]any{"shortcode": code, "__relay_internal__pv__PolarisAIGMMediaWebLabelEnabledrelayprovider": false})
	if err != nil {
		return nil, err
	}
	if status := Text(payload["status"]); status != "" && status != "ok" {
		return nil, fmt.Errorf("instagram: abnormal GraphQL status")
	}
	if payload["data"] == nil && len(Array(payload["errors"])) > 0 {
		if err := overseasInstagramRuling(ctx, c, code, cookie); err != nil {
			return nil, err
		}
	}
	data := Obj(payload["data"])
	if data == nil {
		return nil, fmt.Errorf("instagram: GraphQL response missing data")
	}
	if media, exists := data["xdt_shortcode_media"]; exists {
		if media == nil {
			return nil, &instagramRestrictedError{reason: "restricted post requires an authorized adult account"}
		}
		if node := Obj(media); node != nil {
			return node, nil
		}
	}
	info := Obj(data["xdt_api__v1__media__shortcode__web_info"])
	if info == nil {
		return nil, fmt.Errorf("instagram: GraphQL response missing shortcode web_info")
	}
	items := Array(info["items"])
	if len(items) == 0 {
		return nil, &instagramRestrictedError{reason: "restricted post requires an authorized adult account"}
	}
	if node := Obj(items[0]); node != nil {
		return node, nil
	}
	return nil, fmt.Errorf("instagram: malformed shortcode media")
}

func overseasInstagram(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	final, err := overseasMetaShare(ctx, c, "instagram", raw)
	if err != nil {
		return nil, err
	}
	code := overseasPathID(final, "p", "reel", "reels", "tv")
	if overseasInstagramPK(code) == "" {
		return nil, fmt.Errorf("instagram: invalid post shortcode")
	}
	product, err := overseasInstagramFetch(ctx, c, code, "")
	var attempts []model.FallbackAttempt
	if restricted, ok := err.(*instagramRestrictedError); ok && cookie != "" {
		attempts = append(attempts, model.FallbackAttempt{Name: "匿名", Error: restricted.Error()})
		product, err = overseasInstagramFetch(ctx, c, code, cookie)
	}
	if err != nil {
		return nil, err
	}
	r := &model.ParseResult{Platform: "instagram", SourceURL: final, Content: overseasInstagramCaption(product), Title: Text(product["title"]), Author: overseasFirst(Path(product, "owner", "username"), Path(product, "user", "username")), FallbackErrors: attempts}
	if err := overseasInstagramMedia(r, product); err != nil {
		return nil, err
	}
	if len(r.Media) == 0 {
		return nil, fmt.Errorf("instagram: post contains no accessible media")
	}
	return overseasFinish(r)
}

var threadsRelayProviders = []string{
	"BarcelonaHasPermalinkIndentation", "BarcelonaIsLoggedIn", "BarcelonaHasPostAuthorNotifControls", "BarcelonaShouldShowFediverseM1Features",
	"BarcelonaHasPermalinkPodcastCard", "BarcelonaHasDearAlgoConsumption", "BarcelonaHasEventBadge", "BarcelonaGenAIRepliesEnabled",
	"BarcelonaIsSearchDiscoveryEnabled", "BarcelonaHasCommunities", "BarcelonaHasGameScoreShare", "BarcelonaHasPublicViewCountCard",
	"BarcelonaHasCommunityEntityCard", "BarcelonaHasScorecardCommunity", "BarcelonaHasSportTeamAllegianceCard", "BarcelonaHasMusic",
	"BarcelonaHasNewspaperLinkStyle", "BarcelonaHasMessaging", "BarcelonaHasPodcastTextFragments", "BarcelonaShouldFulfillLightboxQuery",
	"BarcelonaHasViewerReplied", "BarcelonaHasPrivateRepliesDeprecation", "BarcelonaHasGhostPostEmojiActivation", "BarcelonaOptionalCookiesEnabled",
	"BarcelonaHasDearAlgoWebProduction", "BarcelonaHasWebFavicons", "BarcelonaIsCrawler", "BarcelonaHasCommunityTopContributors",
	"BarcelonaCanSeeSponsoredContent", "BarcelonaShouldShowFediverseM075Features", "BarcelonaIsInternalUser",
}

func overseasThreadsMedia(r *model.ParseResult, node map[string]any, depth int) error {
	if depth > 80 {
		return fmt.Errorf("threads: inline media nesting too deep")
	}
	switch int(Number(node["media_type"])) {
	case 19:
		if linked := Obj(Path(node, "text_post_app_info", "linked_inline_media")); linked != nil {
			return overseasThreadsMedia(r, linked, depth+1)
		}
		return nil
	case 8:
		for _, entry := range Array(node["carousel_media"]) {
			child := Obj(entry)
			if child == nil {
				return fmt.Errorf("threads: invalid carousel media")
			}
			// Carousel children are classified by their versions, as in the provider.
			kind := 1
			if len(Array(child["video_versions"])) > 0 {
				kind = 2
			}
			if err := overseasThreadsLeaf(r, child, kind, true); err != nil {
				return err
			}
		}
		return nil
	case 1, 2:
		return overseasThreadsLeaf(r, node, int(Number(node["media_type"])), false)
	default:
		return nil
	}
}

func overseasThreadsLeaf(r *model.ParseResult, node map[string]any, kind int, carousel bool) error {
	image := overseasMetaFirst(Path(node, "image_versions2", "candidates"))
	imageURL := overseasURL(image["url"])
	if imageURL == "" {
		return fmt.Errorf("threads: image or thumbnail missing")
	}
	width, height := int(Number(image["width"])), int(Number(image["height"]))
	if kind == 2 || carousel {
		width, height = int(Number(node["original_width"])), int(Number(node["original_height"]))
	}
	media := model.Media{Kind: "image", URL: imageURL, ThumbnailURL: imageURL, Width: width, Height: height}
	if kind == 2 {
		video := overseasMetaFirst(node["video_versions"])
		media.URL = overseasURL(video["url"])
		if media.URL == "" {
			return fmt.Errorf("threads: video stream missing")
		}
		media.Kind, media.Ext = "video", "mp4"
	}
	overseasAdd(r, media)
	return nil
}

func overseasThreads(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	final, err := overseasMetaShare(ctx, c, "threads", raw)
	if err != nil {
		return nil, err
	}
	code := overseasPathID(final, "post")
	pk := overseasMetaPK(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_")
	if pk == "" {
		return nil, fmt.Errorf("threads: invalid post shortcode")
	}
	s := overseasMetaSession(c, "threads", cookie)
	if err := s.csrf(ctx, false); err != nil {
		return nil, err
	}
	variables := map[string]any{"postID": pk}
	for _, name := range threadsRelayProviders {
		variables["__relay_internal__pv__"+name+"relayprovider"] = false
	}
	payload, err := s.graphql(ctx, "27419285281047858", variables)
	if err != nil {
		return nil, err
	}
	if len(Array(payload["errors"])) > 0 {
		return nil, fmt.Errorf("threads: GraphQL returned errors; content may require an authorized cookie")
	}
	var post map[string]any
	for _, edge := range Array(Path(payload, "data", "data", "edges")) {
		for _, item := range Array(Path(edge, "node", "thread_items")) {
			candidate := Obj(Path(item, "post"))
			if candidate == nil {
				continue
			}
			if post == nil {
				post = candidate
			}
			if Text(candidate["code"]) == code {
				post = candidate
				break
			}
		}
		if Text(post["code"]) == code {
			break
		}
	}
	if post == nil {
		return nil, fmt.Errorf("threads: post metadata missing")
	}
	r := &model.ParseResult{Platform: "threads", SourceURL: final, Content: overseasInstagramCaption(post), Author: Text(Path(post, "user", "username"))}
	if err := overseasThreadsMedia(r, post, 0); err != nil {
		return nil, err
	}
	return overseasFinish(r)
}
