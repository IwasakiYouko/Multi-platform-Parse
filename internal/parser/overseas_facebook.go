package parser

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"multiparse/internal/model"
)

func init() { Register("facebook", overseasFacebook) }

var overseasFacebookField = regexp.MustCompile(`(?s)["'](?:hd_src_no_ratelimit|hd_src|sd_src_no_ratelimit|sd_src|playable_url_quality_hd|playable_url|browser_native_hd_url|browser_native_sd_url)["']\s*:\s*("(?:\\.|[^"\\])*")`)

type overseasMPD struct {
	BaseURL string `xml:"BaseURL"`
	Periods []struct {
		BaseURL string `xml:"BaseURL"`
		Sets    []struct {
			ContentType     string     `xml:"contentType,attr"`
			MimeType        string     `xml:"mimeType,attr"`
			BaseURL         string     `xml:"BaseURL"`
			Protected       []struct{} `xml:"ContentProtection"`
			Representations []struct {
				MimeType  string     `xml:"mimeType,attr"`
				BaseURL   string     `xml:"BaseURL"`
				Width     int        `xml:"width,attr"`
				Height    int        `xml:"height,attr"`
				Bandwidth int64      `xml:"bandwidth,attr"`
				Protected []struct{} `xml:"ContentProtection"`
			} `xml:"Representation"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

func overseasFacebookDASH(body []byte, base string) (model.Media, error) {
	var mpd overseasMPD
	if err := xml.Unmarshal(body, &mpd); err != nil {
		return model.Media{}, fmt.Errorf("facebook: invalid native DASH manifest: %w", err)
	}
	best := model.Media{Kind: "video", Ext: "mp4"}
	var audio string
	var audioRate int64
	resolve := func(parent, child string) string {
		if child == "" {
			return parent
		}
		return Absolute(parent, strings.TrimSpace(child))
	}
	root := resolve(base, mpd.BaseURL)
	for _, period := range mpd.Periods {
		for _, set := range period.Sets {
			if len(set.Protected) > 0 {
				return model.Media{}, fmt.Errorf("facebook: DRM-encrypted video is unsupported")
			}
			parent := resolve(resolve(root, period.BaseURL), set.BaseURL)
			for _, rep := range set.Representations {
				if len(rep.Protected) > 0 {
					return model.Media{}, fmt.Errorf("facebook: DRM-encrypted video is unsupported")
				}
				if strings.TrimSpace(rep.BaseURL) == "" {
					continue
				}
				address := resolve(parent, rep.BaseURL)
				mime := overseasFirst(rep.MimeType, set.MimeType, set.ContentType)
				if strings.HasPrefix(mime, "audio") {
					if rep.Bandwidth > audioRate {
						audio = address
						audioRate = rep.Bandwidth
					}
					continue
				}
				if strings.HasPrefix(mime, "video") && (best.URL == "" || rep.Height > best.Height) {
					best.URL = address
					best.Width = rep.Width
					best.Height = rep.Height
				}
			}
		}
	}
	if best.URL == "" || audio == "" {
		return model.Media{}, fmt.Errorf("facebook: DASH manifest has no directly addressable video/audio pair")
	}
	best.AudioURL = audio
	return best, nil
}
func overseasFacebook(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	body, final, err := Get(ctx, c, raw, Headers(cookie, "https://www.facebook.com/"))
	if err != nil {
		return nil, fmt.Errorf("facebook: %w", err)
	}
	id := overseasPathID(final, "videos", "reel")
	if parsed, e := url.Parse(final); e == nil && id == "" {
		id = parsed.Query().Get("v")
	}
	type candidate struct {
		node  map[string]any
		exact bool
	}
	var candidates []candidate
	for _, state := range overseasStates(body) {
		overseasWalk(state, func(m map[string]any) {
			if m["playable_url"] == nil && m["playable_url_quality_hd"] == nil && m["playable_url_dash"] == nil && m["browser_native_hd_url"] == nil && m["browser_native_sd_url"] == nil && m["videoDeliveryResponseFragment"] == nil && m["videoDeliveryLegacyFields"] == nil && m["dash_manifest"] == nil {
				return
			}
			nodeID := overseasFirst(m["videoId"], m["id"])
			candidates = append(candidates, candidate{m, id != "" && nodeID == id})
		})
	}
	r := &model.ParseResult{Platform: "facebook", SourceURL: raw}
	exact := false
	for _, item := range candidates {
		exact = exact || item.exact
	}
	for _, item := range candidates {
		if exact && !item.exact {
			continue
		}
		node := item.node
		fields := Obj(node["videoDeliveryLegacyFields"])
		if fields == nil {
			fields = node
		}
		address := overseasFirst(fields["playable_url_quality_hd"], fields["browser_native_hd_url"], fields["playable_url"], fields["browser_native_sd_url"])
		fragment := Path(node, "videoDeliveryResponseFragment", "videoDeliveryResponseResult")
		if address == "" {
			if selected := overseasBest(Path(fragment, "progressive_urls"), "progressive_url"); selected != nil {
				address = Text(selected["progressive_url"])
			}
		}
		m := model.Media{Kind: "video", URL: address, Ext: "mp4", Duration: Number(node["playable_duration_in_ms"]) / 1000, ThumbnailURL: overseasFirst(Path(node, "thumbnailImage", "uri"), Path(node, "preferred_thumbnail", "image", "uri"))}
		if m.Duration == 0 {
			m.Duration = Number(node["length_in_second"])
		}
		if m.URL == "" {
			manifest := Text(fields["dash_manifest"])
			if manifest == "" {
				for _, value := range Array(Path(fragment, "dash_manifests")) {
					if s := Text(Path(value, "manifest_xml")); s != "" {
						manifest = s
						break
					}
				}
			}
			if manifest == "" {
				manifestURL := overseasURL(fields["playable_url_dash"])
				if manifestURL == "" {
					for _, value := range Array(Path(fragment, "dash_manifest_urls")) {
						if s := overseasURL(Path(value, "manifest_url")); s != "" {
							manifestURL = s
							break
						}
					}
				}
				if manifestURL != "" {
					data, manifestFinal, fetchErr := Get(ctx, c, manifestURL, overseasHeaders(final))
					if fetchErr != nil {
						return nil, fmt.Errorf("facebook: native DASH fetch failed: %w", fetchErr)
					}
					manifest = string(data)
					final = manifestFinal
				}
			}
			if manifest != "" {
				if address := overseasURL(manifest); address != "" {
					data, manifestFinal, fetchErr := Get(ctx, c, address, overseasHeaders(final))
					if fetchErr != nil {
						return nil, fetchErr
					}
					manifest = string(data)
					final = manifestFinal
				}
				if !strings.Contains(manifest, "<MPD") && strings.Contains(manifest, "&lt;") {
					manifest = html.UnescapeString(manifest)
				}
				m, err = overseasFacebookDASH([]byte(manifest), final)
				if err != nil {
					return nil, err
				}
			} else {
				for _, value := range Array(Path(fragment, "hls_playlist_urls")) {
					if s := overseasURL(Path(value, "hls_playlist_url")); s != "" {
						m.URL = s
						break
					}
				}
			}
		}
		if m.URL == "" {
			continue
		}
		r.Title = overseasFirst(node["name"], r.Title)
		r.Content = overseasFirst(Path(node, "savable_description", "text"), Path(node, "creation_story", "message", "text"), r.Content)
		r.Author = overseasFirst(Path(node, "owner", "name"), r.Author)
		overseasAdd(r, m)
		// Without a matching ID, the first playable node is the page's primary video;
		// later relay nodes can be recommendations, not part of the requested post.
		if !exact {
			break
		}
	}
	if len(r.Media) == 0 {
		for _, match := range overseasFacebookField.FindAllSubmatch(body, -1) {
			var address string
			if json.Unmarshal(match[1], &address) == nil && overseasURL(address) != "" {
				overseasAdd(r, model.Media{Kind: "video", URL: html.UnescapeString(address), Ext: "mp4"})
				break
			}
		}
	}
	if len(r.Media) == 0 {
		if strings.Contains(final, "/login") || strings.Contains(string(body), "id=\"login_form\"") || strings.Contains(string(body), "You must log in to continue") {
			return nil, fmt.Errorf("facebook: login required for this video")
		}
		return nil, fmt.Errorf("facebook: no accessible native playable fields; video is private, removed, restricted, or DRM-protected")
	}
	overseasMetadata(body, r)
	return overseasFinish(r)
}
