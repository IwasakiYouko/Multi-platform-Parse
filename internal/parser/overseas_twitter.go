package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"multiparse/internal/model"
)

func init() { Register("twitter", overseasTwitter) }

const overseasTwitterBearer = "AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"

func overseasTwitterToken(id string) string {
	n, _ := strconv.ParseFloat(id, 64)
	n = n / 1e15 * math.Pi
	whole := uint64(n)
	result := []byte(strconv.FormatUint(whole, 36) + ".")
	fraction := n - float64(whole)
	tolerance := (math.Nextafter(n, math.Inf(1)) - n) / 2
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	for i := 0; i < 64 && fraction > 0; i++ {
		fraction *= 36
		tolerance *= 36
		digit := int(fraction)
		fraction -= float64(digit)
		result = append(result, digits[digit])
		below, above := fraction < tolerance, fraction+tolerance > 1
		if below || above {
			if above && (!below || fraction > 0.5) {
				for j := len(result) - 1; j >= 0; j-- {
					if result[j] == '.' {
						continue
					}
					value := strings.IndexByte(digits, result[j])
					if value < 35 {
						result[j] = digits[value+1]
						break
					}
					result[j] = '0'
				}
			}
			break
		}
	}
	return strings.NewReplacer("0", "", ".", "").Replace(string(result))
}
func overseasTwitterRequest(ctx context.Context, c *http.Client, endpoint, cookie string, params map[string]any) (any, error) {
	headers := Headers(cookie, "https://x.com/")
	headers["Authorization"] = "Bearer " + overseasTwitterBearer
	if cookie != "" {
		request := &http.Request{Header: http.Header{"Cookie": []string{cookie}}}
		for _, item := range request.Cookies() {
			if item.Name == "ct0" {
				headers["x-csrf-token"] = item.Value
			}
		}
		headers["x-twitter-auth-type"] = "OAuth2Session"
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.x.com/1.1/guest/activate.json", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", headers["Authorization"])
		req.Header.Set("User-Agent", headers["User-Agent"])
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("twitter: guest API returned HTTP %d", resp.StatusCode)
		}
		var guest struct {
			Token string `json:"guest_token"`
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&guest); err != nil {
			return nil, err
		}
		if guest.Token == "" {
			return nil, fmt.Errorf("twitter: guest API did not issue a token")
		}
		headers["x-guest-token"] = guest.Token
	}
	variables, _ := json.Marshal(params)
	features, _ := json.Marshal(map[string]bool{
		"creator_subscriptions_tweet_preview_api_enabled": true, "tweetypie_unmention_optimization_enabled": true,
		"responsive_web_edit_tweet_api_enabled": true, "graphql_is_translatable_rweb_tweet_is_translatable_enabled": true,
		"view_counts_everywhere_api_enabled": true, "longform_notetweets_consumption_enabled": true,
		"responsive_web_twitter_article_tweet_consumption_enabled": true, "tweet_awards_web_tipping_enabled": false,
		"freedom_of_speech_not_reach_fetch_enabled": true, "standardized_nudges_misinfo": true,
		"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true,
		"longform_notetweets_rich_text_read_enabled":                              true, "longform_notetweets_inline_media_enabled": true,
		"responsive_web_graphql_exclude_directive_enabled": true, "verified_phone_label_enabled": false,
		"responsive_web_media_download_video_enabled": false, "responsive_web_graphql_skip_user_profile_image_extensions_enabled": false,
		"responsive_web_graphql_timeline_navigation_enabled": true, "responsive_web_enhance_cards_enabled": false,
		"articles_api_enabled": true, "articles_preview_enabled": true, "longform_notetweets_richtext_consumption_enabled": true,
		"communities_web_enable_tweet_community_results_fetch": true, "responsive_web_jetfuel_frame": true,
		"responsive_web_grok_image_annotation_enabled": false, "responsive_web_grok_imagine_annotation_enabled": false,
		"responsive_web_grok_analyze_post_followups_enabled": false, "responsive_web_grok_share_attachment_enabled": false,
		"responsive_web_grok_analysis_button_from_backend": false, "responsive_web_grok_show_grok_translated_post": false,
		"responsive_web_grok_community_note_auto_translation_is_enabled": false, "responsive_web_grok_annotations_enabled": false,
		"rweb_video_screen_enabled": false, "rweb_video_timestamps_enabled": false, "premium_content_api_read_enabled": false,
		"creator_subscriptions_quote_tweet_preview_enabled": false, "c9s_tweet_anatomy_moderator_badge_enabled": true,
		"rweb_tipjar_consumption_enabled": true, "profile_label_improvements_pcf_label_in_post_enabled": true,
	})
	query := url.Values{"variables": {string(variables)}, "features": {string(features)}, "fieldToggles": {"{\"withArticleRichContentState\":true,\"withArticlePlainText\":true,\"withGrokAnalyze\":false,\"withDisallowedReplyControls\":false}"}}
	var data any
	err := JSON(ctx, c, "https://x.com/i/api/graphql/"+endpoint+"?"+query.Encode(), headers, &data)
	if err != nil {
		return nil, err
	}
	return data, nil
}
func overseasTwitterGraph(ctx context.Context, c *http.Client, id, cookie string) (any, error) {
	endpoint := "2ICDjqPd81tulZcYrtpTuQ/TweetResultByRestId"
	variables := map[string]any{"tweetId": id, "withCommunity": false, "includePromotedContent": false, "withVoice": false}
	if cookie != "" {
		endpoint = "meGUdoK_ryVZ0daBK-HJ2g/TweetDetail"
		variables = map[string]any{"focalTweetId": id, "with_rux_injections": false, "includePromotedContent": false, "withCommunity": true, "withQuickPromoteEligibilityTweetFields": false, "withBirdwatchNotes": true, "withVoice": true}
	}
	data, err := overseasTwitterRequest(ctx, c, endpoint, cookie, variables)
	if err != nil {
		return nil, err
	}
	result := Path(data, "data", "tweetResult", "result")
	if cookie != "" {
		overseasWalk(data, func(m map[string]any) {
			if Text(m["rest_id"]) == id && m["legacy"] != nil {
				result = m
			}
		})
	}
	if result == nil {
		return nil, fmt.Errorf("twitter: API returned no tweet; login or access permission may be required")
	}
	if Text(Path(result, "__typename")) == "TweetWithVisibilityResults" {
		result = Path(result, "tweet")
	}
	if reason := overseasFirst(Path(result, "reason"), Path(result, "tombstone", "text", "text")); reason != "" {
		return nil, fmt.Errorf("twitter: %s", reason)
	}
	return result, nil
}
func overseasTwitterArticleLookup(ctx context.Context, c *http.Client, id, cookie string) (any, error) {
	if cookie == "" {
		return nil, fmt.Errorf("twitter: reading full articles requires an authorized X session cookie")
	}
	data, err := overseasTwitterRequest(ctx, c, "yIphfmxUO-hddQHKIOk9tA/SearchTimeline", cookie, map[string]any{"rawQuery": "url:\"x.com/i/article/" + id + "\"", "count": 20, "querySource": "typed_query", "product": "Latest"})
	if err != nil {
		return nil, err
	}
	tweetID := ""
	overseasWalk(data, func(m map[string]any) {
		if tweetID != "" || m["article"] == nil || m["legacy"] == nil {
			return
		}
		overseasWalk(m["article"], func(article map[string]any) {
			if overseasFirst(article["rest_id"], article["id"]) == id {
				tweetID = Text(m["rest_id"])
			}
		})
	})
	if tweetID == "" {
		return nil, fmt.Errorf("twitter: authenticated search found no indexed linking post for this article; this does not prove it was deleted")
	}
	result, err := overseasTwitterGraph(ctx, c, tweetID, cookie)
	if err != nil {
		return nil, err
	}
	matched := false
	overseasWalk(Path(result, "article"), func(article map[string]any) {
		if overseasFirst(article["rest_id"], article["id"]) == id {
			matched = true
		}
	})
	if !matched {
		return nil, fmt.Errorf("twitter: article ID did not match the linking post returned by X")
	}
	return result, nil
}
func overseasTwitter(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	id := overseasPathID(raw, "status", "statuses", "article")
	if id == "" {
		return nil, fmt.Errorf("twitter: provide a post or article URL, not a profile or live broadcast")
	}
	_, idErr := strconv.ParseUint(id, 10, 64)
	if idErr != nil {
		return nil, fmt.Errorf("twitter: invalid post ID")
	}
	var state any
	var graphErr error
	directArticle := strings.Contains(raw, "/article/")
	if directArticle {
		state, graphErr = overseasTwitterArticleLookup(ctx, c, id, cookie)
		if graphErr != nil {
			return nil, graphErr
		}
	} else if cookie != "" {
		state, graphErr = overseasTwitterGraph(ctx, c, id, cookie)
	}
	if state == nil {
		var synd any
		err := JSON(ctx, c, "https://cdn.syndication.twimg.com/tweet-result?"+url.Values{"id": {id}, "lang": {"en"}, "token": {overseasTwitterToken(id)}}.Encode(), Headers("", "https://platform.twitter.com/"), &synd)
		if err == nil && len(Obj(synd)) > 0 {
			state = synd
		} else if graphErr == nil {
			state, graphErr = overseasTwitterGraph(ctx, c, id, cookie)
		}
	}
	if state == nil {
		return nil, fmt.Errorf("twitter: protected, deleted, rate-limited, or unavailable post: %v", graphErr)
	}
	r := &model.ParseResult{Platform: "twitter", SourceURL: raw}
	r.Content = overseasFirst(Path(state, "note_tweet", "note_tweet_results", "result", "text"), Path(state, "legacy", "full_text"), Path(state, "full_text"), Path(state, "text"))
	r.Author = overseasFirst(Path(state, "core", "user_results", "result", "legacy", "name"), Path(state, "user", "name"))
	// Article content is Draft.js rich text, not the short teaser on the tweet.
	articlePresent := directArticle || Path(state, "article") != nil
	fullArticle := false
	overseasWalk(state, func(m map[string]any) {
		if blocks := Array(m["blocks"]); len(blocks) > 0 {
			var paragraphs []string
			for _, block := range blocks {
				if text := Text(Path(block, "text")); text != "" {
					paragraphs = append(paragraphs, text)
				}
			}
			if len(paragraphs) > 0 {
				r.Content = strings.Join(paragraphs, "\n\n")
				fullArticle = true
			}
		}
		if Text(m["title"]) != "" && (m["content_state"] != nil || m["plain_text"] != nil) {
			r.Title = Text(m["title"])
			if s := Text(m["plain_text"]); s != "" {
				r.Content = s
				fullArticle = true
			}
		}
	})
	if articlePresent && !fullArticle {
		return nil, fmt.Errorf("twitter: X exposed only the article preview, not its complete body; an authorized session is required")
	}
	expectedVideo := false
	overseasWalk(state, func(m map[string]any) {
		kind := Text(m["type"])
		if kind == "photo" {
			image := overseasFirst(m["media_url_https"], m["media_url"], m["url"])
			if image != "" {
				overseasAdd(r, model.Media{Kind: "image", URL: image, Width: int(Number(Path(m, "original_info", "width"))), Height: int(Number(Path(m, "original_info", "height")))})
			}
		}
		variants := Path(m, "video_info", "variants")
		if variants == nil && m["variants"] != nil && (m["durationMs"] != nil || m["poster"] != nil) {
			variants = m["variants"]
		}
		if kind == "video" || kind == "animated_gif" {
			expectedVideo = true
		}
		if variants != nil {
			var selected map[string]any
			var score float64 = -1
			for _, v := range Array(variants) {
				f := Obj(v)
				u := overseasFirst(f["url"], f["src"])
				if overseasURL(u) == "" {
					continue
				}
				s := Number(f["bitrate"]) + Number(f["bit_rate"])
				if strings.Contains(u, ".mp4") {
					s += 1e12
				}
				if s > score {
					selected = f
					score = s
				}
			}
			if selected != nil {
				overseasAdd(r, model.Media{Kind: "video", URL: overseasFirst(selected["url"], selected["src"]), ThumbnailURL: overseasFirst(m["media_url_https"], m["poster"]), Duration: Number(Path(m, "video_info", "duration_millis")) / 1000})
			}
		}
		if m["original_img_url"] != nil {
			overseasAdd(r, model.Media{Kind: "image", URL: Text(m["original_img_url"]), Width: int(Number(m["original_img_width"])), Height: int(Number(m["original_img_height"]))})
		}
	})
	if expectedVideo {
		hasVideo := false
		for _, m := range r.Media {
			hasVideo = hasVideo || m.Kind == "video"
		}
		if !hasVideo {
			return nil, fmt.Errorf("twitter: post contains video but API did not expose an accessible stream; authentication may be required")
		}
	}
	result, err := overseasFinish(r)
	if err == nil && fullArticle {
		result.Type = "article"
	}
	return result, err
}
