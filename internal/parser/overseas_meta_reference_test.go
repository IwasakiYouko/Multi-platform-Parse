package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func referenceMetaHomepage(req *http.Request) *http.Response {
	resp := referenceSocialResponse(req, "homepage")
	resp.Header.Add("Set-Cookie", "csrftoken=anonymous-token; Path=/; Secure")
	return resp
}

func referenceMetaVariables(t *testing.T, req *http.Request, host, doc, token string) map[string]any {
	t.Helper()
	if req.Method != "POST" || req.URL.String() != "https://www."+host+"/graphql/query" {
		t.Fatalf("wrong GraphQL request: %s %s", req.Method, req.URL)
	}
	if req.Header.Get("X-CSRFToken") != token {
		t.Fatalf("CSRF token missing: %q", req.Header.Get("X-CSRFToken"))
	}
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if req.PostForm.Get("doc_id") != doc || req.PostForm.Get("server_timestamps") != "true" || len(req.PostForm) != 3 {
		t.Fatalf("incorrect GraphQL form: %v", req.PostForm)
	}
	var variables map[string]any
	if err := json.Unmarshal([]byte(req.PostForm.Get("variables")), &variables); err != nil {
		t.Fatal(err)
	}
	return variables
}

func referenceInstagramVariables(t *testing.T, req *http.Request, token string) {
	t.Helper()
	vars := referenceMetaVariables(t, req, "instagram.com", "27128499623469141", token)
	want := map[string]any{"shortcode": "AbC", "__relay_internal__pv__PolarisAIGMMediaWebLabelEnabledrelayprovider": false}
	if !reflect.DeepEqual(vars, want) {
		t.Fatalf("incorrect Instagram variables: %#v", vars)
	}
}

const referenceInstagramCarousel = `{"data":{"xdt_api__v1__media__shortcode__web_info":{"items":[{"code":"AbC","media_type":8,"caption":{"text":"carousel caption"},"carousel_media":[{"media_type":1,"image_versions2":{"candidates":[{"url":"https://cdn.example/first.jpg","width":10,"height":20},{"url":"https://cdn.example/not-best.jpg","width":100,"height":200}]}},{"media_type":2,"image_versions2":{"candidates":[{"url":"https://cdn.example/poster.jpg","width":30,"height":40}]},"video_versions":[{"url":"https://cdn.example/first.mp4","width":30,"height":40},{"url":"https://cdn.example/not-best.mp4","width":300,"height":400}]}]}]}}}`

func TestReferenceInstagramAnonymousFirstAndShareRouting(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if ck, _ := req.Cookie("sessionid"); ck != nil && ck.Value == "configured-secret" {
			t.Fatal("configured cookie used before anonymous restriction")
		}
		switch calls {
		case 1:
			if req.URL.String() != "https://www.instagram.com/share/reel/share-id/" {
				t.Fatalf("share endpoint: %s", req.URL)
			}
			resp := referenceSocialResponse(req, "")
			resp.StatusCode = 302
			resp.Header.Set("Location", "https://www.instagram.com/reel/AbC/")
			return resp, nil
		case 2:
			if req.URL.Path != "/reel/AbC/" {
				t.Fatalf("share redirect: %s", req.URL)
			}
			return referenceSocialResponse(req, "post routing only"), nil
		case 3:
			if req.URL.String() != "https://www.instagram.com/" {
				t.Fatalf("CSRF homepage: %s", req.URL)
			}
			return referenceMetaHomepage(req), nil
		case 4:
			referenceInstagramVariables(t, req, "anonymous-token")
			return referenceSocialResponse(req, referenceInstagramCarousel), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
	})}
	result, err := overseasInstagram(context.Background(), client, "https://www.instagram.com/share/reel/share-id/", "sessionid=configured-secret; csrftoken=configured-token")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 || len(result.Media) != 2 || result.Content != "carousel caption" || result.Media[0].URL != "https://cdn.example/first.jpg" || result.Media[1].URL != "https://cdn.example/first.mp4" || result.Media[1].Width != 30 {
		t.Fatalf("wrong source order or media: %#v", result)
	}
	if result.SourceURL != "https://www.instagram.com/reel/AbC/" || len(result.FallbackErrors) != 0 {
		t.Fatalf("bad routing/fallback: %#v", result)
	}
	for _, media := range result.Media {
		if media.Headers["Cookie"] != "" {
			t.Fatal("session cookie leaked to CDN")
		}
	}
}

func TestReferenceInstagramRestrictedOnlyCookieFallback(t *testing.T) {
	for _, test := range []struct {
		name, response, ruling string
		fallback, unavailable  bool
	}{
		{"null restricted media", `{"data":{"xdt_shortcode_media":null}}`, "", true, false},
		{"ruling restricted", `{"data":null,"errors":[{"message":"blocked"}]}`, `{"title":"Age restricted","description":"Please sign in"}`, true, false},
		{"ruling missing", `{"data":null,"errors":[{"message":"missing"}]}`, `{"message":"Media cannot be found"}`, false, true},
		{"ordinary GraphQL error", `{"data":null,"errors":[{"message":"execution failed"}]}`, `{}`, false, false},
		{"malformed data", `{"data":{}}`, "", false, false},
		{"abnormal status", `{"status":"fail","data":{"xdt_shortcode_media":null}}`, "", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			queries, rulingCalls := 0, 0
			client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() == "https://www.instagram.com/" && req.Method == "GET" {
					return referenceMetaHomepage(req), nil
				}
				if req.URL.Path == "/api/v1/web/get_ruling_for_content/" {
					rulingCalls++
					if test.ruling == "" || req.Method != "GET" || req.URL.Query().Get("content_type") != "MEDIA" || req.URL.Query().Get("target_id") != "1730" || len(req.URL.Query()) != 2 {
						t.Fatalf("incorrect ruling request: %s", req.URL)
					}
					if req.Header.Get("X-IG-App-ID") != "936619743392459" || req.Header.Get("X-CSRFToken") != "anonymous-token" {
						t.Fatal("ruling headers missing")
					}
					return referenceSocialResponse(req, test.ruling), nil
				}
				queries++
				if queries == 1 {
					referenceInstagramVariables(t, req, "anonymous-token")
					if ck, _ := req.Cookie("sessionid"); ck != nil && ck.Value != "" {
						t.Fatal("anonymous attempt sent session cookie")
					}
					return referenceSocialResponse(req, test.response), nil
				}
				if !test.fallback || queries != 2 {
					t.Fatalf("ordinary error incorrectly retried with credentials: %s", req.URL)
				}
				referenceInstagramVariables(t, req, "configured-token")
				ck, err := req.Cookie("sessionid")
				if err != nil || ck.Value != "configured-secret" {
					t.Fatal("authorized retry lacks configured cookie")
				}
				return referenceSocialResponse(req, `{"data":{"xdt_shortcode_media":{"__typename":"XDTGraphImage","shortcode":"AbC","display_url":"https://cdn.example/photo.jpg","dimensions":{"width":20,"height":30},"caption":"caption"}}}`), nil
			})}
			result, err := overseasInstagram(context.Background(), client, "https://www.instagram.com/p/AbC/", "sessionid=configured-secret; csrftoken=configured-token")
			if test.fallback {
				if err != nil {
					t.Fatal(err)
				}
				if queries != 2 || len(result.FallbackErrors) != 1 || result.Media[0].URL != "https://cdn.example/photo.jpg" {
					t.Fatalf("cookie fallback not preserved: %#v", result)
				}
			} else {
				if err == nil || queries != 1 {
					t.Fatalf("expected terminal ordinary error: queries=%d result=%#v error=%v", queries, result, err)
				}
				if test.unavailable && !referenceUnavailable(err) {
					t.Fatalf("missing content classification lost: %v", err)
				}
			}
			if test.ruling != "" && rulingCalls != 1 {
				t.Fatalf("ruling not queried: %d", rulingCalls)
			}
		})
	}
}

func TestReferenceInstagramMissingCSRFAndMedia(t *testing.T) {
	queries := 0
	client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://www.instagram.com/" {
			t.Fatalf("query must not run without csrf: %s", req.URL)
		}
		return referenceSocialResponse(req, "no csrf"), nil
	})}
	if _, err := overseasInstagram(context.Background(), client, "https://www.instagram.com/p/AbC/", "sessionid=secret"); err == nil {
		t.Fatal("accepted missing CSRF")
	}
	for _, node := range []string{`{"__typename":"GraphVideo","is_video":true,"display_url":"https://cdn.example/poster.jpg"}`, `{"__typename":"GraphImage"}`, `{"__typename":"GraphSidecar","edge_sidecar_to_children":{"edges":[]}}`} {
		client.Transport = referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() == "https://www.instagram.com/" {
				return referenceMetaHomepage(req), nil
			}
			queries++
			referenceInstagramVariables(t, req, "anonymous-token")
			return referenceSocialResponse(req, `{"data":{"xdt_shortcode_media":`+node+`}}`), nil
		})
		if _, err := overseasInstagram(context.Background(), client, "https://www.instagram.com/p/AbC/", "sessionid=secret; csrftoken=secret-token"); err == nil {
			t.Fatalf("accepted missing media: %s", node)
		}
	}
	if queries != 3 {
		t.Fatalf("malformed media must not trigger cookie fallback: %d", queries)
	}
}

func referenceThreadsVariables(t *testing.T, req *http.Request, token string) {
	t.Helper()
	vars := referenceMetaVariables(t, req, "threads.com", "27419285281047858", token)
	if vars["postID"] != "1730" || len(vars) != 32 {
		t.Fatalf("incorrect Threads variables: %#v", vars)
	}
	// Independent list catches missing providers, rather than repeating the implementation's slice.
	names := strings.Fields(`BarcelonaHasPermalinkIndentation BarcelonaIsLoggedIn BarcelonaHasPostAuthorNotifControls BarcelonaShouldShowFediverseM1Features BarcelonaHasPermalinkPodcastCard BarcelonaHasDearAlgoConsumption BarcelonaHasEventBadge BarcelonaGenAIRepliesEnabled BarcelonaIsSearchDiscoveryEnabled BarcelonaHasCommunities BarcelonaHasGameScoreShare BarcelonaHasPublicViewCountCard BarcelonaHasCommunityEntityCard BarcelonaHasScorecardCommunity BarcelonaHasSportTeamAllegianceCard BarcelonaHasMusic BarcelonaHasNewspaperLinkStyle BarcelonaHasMessaging BarcelonaHasPodcastTextFragments BarcelonaShouldFulfillLightboxQuery BarcelonaHasViewerReplied BarcelonaHasPrivateRepliesDeprecation BarcelonaHasGhostPostEmojiActivation BarcelonaOptionalCookiesEnabled BarcelonaHasDearAlgoWebProduction BarcelonaHasWebFavicons BarcelonaIsCrawler BarcelonaHasCommunityTopContributors BarcelonaCanSeeSponsoredContent BarcelonaShouldShowFediverseM075Features BarcelonaIsInternalUser`)
	for _, name := range names {
		value, exists := vars["__relay_internal__pv__"+name+"relayprovider"]
		if !exists || value != false {
			t.Fatalf("missing relay provider %s", name)
		}
	}
	if req.Header.Get("X-IG-App-ID") != "238260118697367" {
		t.Fatal("Threads app ID missing")
	}
}

func referenceThreadsPayload(post string) string {
	return `{"data":{"data":{"edges":[{"node":{"thread_items":[{"post":{"code":"wrong","caption":{"text":"wrong"},"media_type":19}},{"post":` + post + `}]}}]}}}`
}

func TestReferenceThreadsShareGraphQLInlineCarousel(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if req.URL.String() != "https://www.threads.com/share/abc" || req.Header.Get("Cookie") != "" {
				t.Fatalf("share must not use session cookies: %s", req.URL)
			}
			resp := referenceSocialResponse(req, "")
			resp.StatusCode = 302
			resp.Header.Set("Location", "https://www.threads.com/@author/post/AbC")
			return resp, nil
		case 2:
			if req.URL.String() != "https://www.threads.com/@author/post/AbC" || req.Header.Get("Cookie") != "" {
				t.Fatalf("share post route/cookie: %s", req.URL)
			}
			return referenceSocialResponse(req, "resolved"), nil
		case 3:
			referenceThreadsVariables(t, req, "configured-token")
			ck, err := req.Cookie("sessionid")
			if err != nil || ck.Value != "secret" {
				t.Fatal("configured Threads session missing")
			}
			return referenceSocialResponse(req, referenceThreadsPayload(`{"code":"AbC","caption":{"text":"inline media"},"media_type":19,"text_post_app_info":{"linked_inline_media":{"media_type":19,"text_post_app_info":{"linked_inline_media":{"media_type":8,"carousel_media":[{"image_versions2":{"candidates":[{"url":"https://cdn.example/first.jpg"},{"url":"https://cdn.example/ignored.jpg"}]},"original_width":10,"original_height":20},{"image_versions2":{"candidates":[{"url":"https://cdn.example/poster.jpg"}]},"video_versions":[{"url":"https://cdn.example/first.mp4"},{"url":"https://cdn.example/ignored.mp4"}],"original_width":30,"original_height":40}]}}}}}`)), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
	})}
	result, err := overseasThreads(context.Background(), client, "https://www.threads.com/share/abc", "sessionid=secret; csrftoken=configured-token")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(result.Media) != 2 || result.Content != "inline media" || result.Media[0].URL != "https://cdn.example/first.jpg" || result.Media[1].URL != "https://cdn.example/first.mp4" || result.Media[1].Width != 30 {
		t.Fatalf("wrong inline carousel: %#v", result)
	}
	for _, media := range result.Media {
		if media.Headers["Cookie"] != "" {
			t.Fatal("Threads cookie leaked to media")
		}
	}
}

func TestReferenceThreadsPhotosVideosTextAndMissingMedia(t *testing.T) {
	for _, test := range []struct {
		name, node, kind string
		count            int
		fail             bool
	}{
		{"photo", `{"media_type":1,"image_versions2":{"candidates":[{"url":"https://cdn.example/image.jpg","width":12,"height":13}]}}`, "image", 1, false},
		{"video", `{"media_type":2,"image_versions2":{"candidates":[{"url":"https://cdn.example/poster.jpg"}]},"video_versions":[{"url":"https://cdn.example/video.mp4"}],"original_width":12,"original_height":13}`, "video", 1, false},
		{"text", `{"media_type":19}`, "article", 0, false},
		{"missing video", `{"media_type":2,"image_versions2":{"candidates":[{"url":"https://cdn.example/poster.jpg"}]}}`, "", 0, true},
		{"missing image", `{"media_type":1,"image_versions2":{"candidates":[]}}`, "", 0, true},
		{"missing poster", `{"media_type":2,"video_versions":[{"url":"https://cdn.example/v.mp4"}]}`, "", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if req.URL.String() != "https://www.threads.com/" {
						t.Fatalf("expected csrf homepage: %s", req.URL)
					}
					// Unlike Instagram, Threads allows no anonymous csrf token.
					return referenceSocialResponse(req, "public homepage"), nil
				}
				referenceThreadsVariables(t, req, "")
				var node map[string]any
				if err := json.Unmarshal([]byte(test.node), &node); err != nil {
					t.Fatal(err)
				}
				node["code"], node["caption"] = "AbC", map[string]string{"text": "body"}
				encoded, _ := json.Marshal(node)
				return referenceSocialResponse(req, referenceThreadsPayload(string(encoded))), nil
			})}
			result, err := overseasThreads(context.Background(), client, "https://www.threads.com/@author/post/AbC", "")
			if test.fail {
				if err == nil {
					t.Fatalf("accepted missing media: %#v", result)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || result.Type != test.kind || len(result.Media) != test.count || result.Content != "body" {
				t.Fatalf("unexpected result: %#v", result)
			}
		})
	}
}

func TestReferenceSocialCredentialsRejectCrossPlatformRedirects(t *testing.T) {
	for _, platform := range []string{"instagram", "threads", "tiktok"} {
		t.Run(platform, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Hostname() != "www."+platform+".com" {
					t.Fatal("credentials or request reached foreign host")
				}
				resp := referenceSocialResponse(req, "")
				resp.StatusCode = 302
				resp.Header.Set("Location", "https://foreign.example/stolen")
				return resp, nil
			})}
			s := newOverseasSession(client, platform+".com", "sessionid=secret")
			s.headers["X-CSRFToken"] = "secret-token"
			if _, _, err := s.request(context.Background(), "GET", "https://www."+platform+".com/share/abc", nil, true); err == nil || calls != 1 {
				t.Fatalf("redirect escaped platform: calls=%d error=%v", calls, err)
			}
			if _, _, err := s.request(context.Background(), "GET", "https://foreign.example/", nil, true); err == nil || calls != 1 {
				t.Fatal("foreign endpoint not rejected")
			}
		})
	}
}
