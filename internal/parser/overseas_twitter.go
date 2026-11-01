package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() { Register("twitter", overseasTwitter) }

const overseasTwitterBearer = "AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"
const overseasTwitterFeatures = `{"creator_subscriptions_tweet_preview_api_enabled":true,"communities_web_enable_tweet_community_results_fetch":true,"c9s_tweet_anatomy_moderator_badge_enabled":true,"tweetypie_unmention_optimization_enabled":true,"responsive_web_edit_tweet_api_enabled":true,"graphql_is_translatable_rweb_tweet_is_translatable_enabled":true,"view_counts_everywhere_api_enabled":true,"longform_notetweets_consumption_enabled":true,"responsive_web_twitter_article_tweet_consumption_enabled":true,"tweet_awards_web_tipping_enabled":false,"creator_subscriptions_quote_tweet_preview_enabled":false,"freedom_of_speech_not_reach_fetch_enabled":true,"standardized_nudges_misinfo":true,"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled":true,"tweet_with_visibility_results_prefer_gql_media_interstitial_enabled":false,"rweb_video_timestamps_enabled":true,"longform_notetweets_rich_text_read_enabled":true,"longform_notetweets_inline_media_enabled":true,"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true,"responsive_web_enhance_cards_enabled":false}`

var twitterStatusPath = regexp.MustCompile(`^/(?:[^/]+/status|i/web/status)/(\d+)(?:/|$)`)
var twitterTrailingMediaLink = regexp.MustCompile(`https://t\.co/[^\s,]+$`)

func twitterInput(raw string) (string, string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
		return "", "", fmt.Errorf("twitter: invalid post URL")
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "mobile.twitter.com" {
		host = "twitter.com"
	}
	if host != "x.com" && host != "twitter.com" && host != "fixupx.com" {
		return "", "", fmt.Errorf("twitter: unsupported post host")
	}
	match := twitterStatusPath.FindStringSubmatch(u.Path)
	if len(match) != 2 {
		return "", "", fmt.Errorf("twitter: provide a status URL; profile, article and live URLs are unsupported")
	}
	u.Scheme, u.Host, u.RawQuery, u.Fragment = "https", "x.com", "", ""
	return u.String(), match[1], nil
}

func twitterSession(cookie string) (string, string) {
	r := &http.Request{Header: http.Header{"Cookie": []string{cookie}}}
	ct0, auth := "", ""
	for _, c := range r.Cookies() {
		switch c.Name {
		case "ct0":
			ct0 = c.Value
		case "auth_token":
			auth = c.Value
		}
	}
	if ct0 == "" || auth == "" {
		return "", ""
	}
	return cookie, ct0
}

func overseasTwitter(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	// Resolve short links without ever attaching an authenticated session to them.
	if u, err := url.Parse(raw); err == nil && strings.EqualFold(u.Hostname(), "t.co") {
		shortClient := *c
		shortClient.Jar = nil
		_, final, err := Get(netx.WithoutCookies(ctx), &shortClient, raw, Headers("", ""))
		if err != nil {
			return nil, err
		}
		raw = final
	}
	source, id, err := twitterInput(raw)
	if err != nil {
		return nil, err
	}
	variables, _ := json.Marshal(map[string]any{"tweetId": id, "withCommunity": false, "includePromotedContent": false, "withVoice": false})
	query := url.Values{"variables": {string(variables)}, "features": {overseasTwitterFeatures}, "fieldToggles": {`{"withArticleRichContentState":true,"withArticlePlainText":false}`}}
	headers := Headers("", "")
	headers["Authorization"] = "Bearer " + overseasTwitterBearer
	headers["Accept-Language"], headers["Content-Type"] = "zh-CN,zh;q=0.9", "application/json"
	headers["x-twitter-active-user"], headers["x-twitter-client-language"] = "yes", "zh-cn"
	if session, csrf := twitterSession(cookie); session != "" {
		headers["Cookie"], headers["x-csrf-token"] = session, csrf
		ctx = netx.WithExplicitCookies(ctx)
	} else {
		ctx = netx.WithoutCookies(ctx)
	}
	client := *c
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var data any
	if err := JSON(ctx, &client, "https://api.twitter.com/graphql/kPLTRmMnzbPTv70___D06w/TweetResultByRestId?"+query.Encode(), headers, &data); err != nil {
		return nil, fmt.Errorf("twitter: %w", err)
	}
	return twitterResult(data, source, id)
}

func twitterResult(data any, source, id string) (*model.ParseResult, error) {
	if errs := Array(Path(data, "errors")); len(errs) > 0 {
		return nil, fmt.Errorf("twitter: API: %s", Text(Path(errs[0], "message")))
	}
	state := Path(data, "data", "tweetResult", "result")
	if state == nil {
		return nil, ContentUnavailableError{Reason: "twitter: post or user does not exist"}
	}
	if wrapped := Path(state, "tweet"); wrapped != nil {
		state = wrapped
	}
	legacy := Path(state, "legacy")
	if legacy == nil {
		reason := Text(Path(state, "reason"))
		if reason == "Suspended" || reason == "BounceDeleted" {
			return nil, ContentUnavailableError{Reason: "twitter: " + reason}
		}
		if Text(Path(state, "__typename")) == "TweetTombstone" {
			return nil, fmt.Errorf("twitter: post is restricted for anonymous viewers")
		}
		return nil, fmt.Errorf("twitter: post is not accessible: %s", reason)
	}
	if restID := Text(Path(state, "rest_id")); restID != "" && restID != id {
		return nil, fmt.Errorf("twitter: API returned a different post")
	}
	r := &model.ParseResult{Platform: "twitter", SourceURL: source, Author: overseasFirst(Path(state, "core", "user_results", "result", "core", "name"), Path(state, "core", "user_results", "result", "legacy", "name"))}
	if article := Path(state, "article", "article_results", "result"); article != nil {
		if err := twitterArticle(r, article); err != nil {
			return nil, err
		}
		result, err := overseasFinish(r)
		if err == nil {
			result.Type = "article"
		}
		return result, err
	}
	r.Content = overseasFirst(Path(state, "note_tweet", "note_tweet_results", "result", "text"), Path(legacy, "full_text"))
	// Only the selected tweet's media array is authoritative, never quoted/replied tweets.
	media := Array(Path(legacy, "entities", "media"))
	for _, item := range media {
		m := model.Media{Width: int(Number(Path(item, "original_info", "width"))), Height: int(Number(Path(item, "original_info", "height")))}
		image := Text(Path(item, "media_url_https"))
		switch Text(Path(item, "type")) {
		case "photo":
			m.Kind, m.URL, m.ThumbnailURL = "image", twitterImageSize(image, "orig"), twitterImageSize(image, "small")
		case "video", "animated_gif":
			variants := Array(Path(item, "video_info", "variants"))
			if len(variants) == 0 {
				return nil, fmt.Errorf("twitter: media has no accessible video variants")
			}
			m.Kind, m.URL, m.ThumbnailURL = "video", Text(Path(variants[len(variants)-1], "url")), twitterImageSize(image, "medium")
			m.Duration = Number(Path(item, "video_info", "duration_millis")) / 1000
			if Text(Path(item, "type")) == "animated_gif" {
				m.Kind, m.ThumbnailURL = "gif", twitterImageSize(image, "small")
			}
		default:
			continue
		}
		if overseasURL(m.URL) == "" {
			return nil, fmt.Errorf("twitter: media URL is missing or invalid")
		}
		overseasAdd(r, m)
	}
	if len(media) > 0 {
		r.Content = twitterTrailingMediaLink.ReplaceAllString(r.Content, "")
	}
	return overseasFinish(r)
}

func twitterImageSize(raw, size string) string {
	u, err := url.Parse(raw)
	if err != nil || overseasURL(raw) == "" {
		return ""
	}
	q := u.Query()
	q.Set("name", size)
	u.RawQuery = q.Encode()
	return u.String()
}
