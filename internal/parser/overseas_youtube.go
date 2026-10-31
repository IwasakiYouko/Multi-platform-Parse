package parser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/kkdai/youtube/v2"
	"multiparse/internal/model"
)

func init() {
	// Android VR currently exposes CDN URLs that reject even bounded ranges.
	// SDK-less Android returns complete streams without sending account cookies.
	youtube.DefaultClient = youtube.AndroidClient
	Register("youtube", overseasYouTube)
}

type overseasYouTubeTransport struct {
	base   http.RoundTripper
	cookie string
	ctx    context.Context
}

func (t overseasYouTubeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(t.ctx)
	host := strings.ToLower(req.URL.Hostname())
	if host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") {
		if t.cookie != "" {
			req.Header.Set("Cookie", t.cookie)
		}
	} else {
		req.Header.Del("Cookie")
	}
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp.Body != nil {
		resp.Body = &overseasLimitedBody{Reader: io.LimitReader(resp.Body, 16<<20), closer: resp.Body}
	}
	return resp, err
}

type overseasLimitedBody struct {
	io.Reader
	closer io.Closer
}

func (b *overseasLimitedBody) Close() error { return b.closer.Close() }
func overseasYouTubeURL(ctx context.Context, client *youtube.Client, video *youtube.Video, format *youtube.Format) (string, error) {
	if direct, err := url.Parse(format.URL); err == nil && direct.Hostname() != "" && direct.Query().Get("n") == "" && direct.Query().Get("v") == "" {
		return direct.String(), nil
	}
	return client.GetStreamURLContext(ctx, video, format)
}

func overseasYouTube(ctx context.Context, client *http.Client, raw, cookie string) (*model.ParseResult, error) {
	if client == nil || client.Transport == nil {
		return nil, fmt.Errorf("youtube: SSRF-safe HTTP transport is required")
	}
	scoped := *client
	scoped.Transport = overseasYouTubeTransport{base: client.Transport, cookie: cookie, ctx: ctx}
	yt := youtube.Client{HTTPClient: &scoped}
	video, err := yt.GetVideoContext(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("youtube: player unavailable, restricted, or signature extraction failed: %w", err)
	}
	var chosen, audio *youtube.Format
	for i := range video.Formats {
		f := &video.Formats[i]
		if strings.HasPrefix(f.MimeType, "audio/") {
			if audio == nil || (strings.Contains(f.MimeType, "audio/mp4") && !strings.Contains(audio.MimeType, "audio/mp4")) || (strings.Contains(f.MimeType, "audio/mp4") == strings.Contains(audio.MimeType, "audio/mp4") && f.Bitrate > audio.Bitrate) {
				audio = f
			}
			continue
		}
		if !strings.HasPrefix(f.MimeType, "video/") {
			continue
		}
		if chosen == nil || f.Height > chosen.Height || (f.Height == chosen.Height && f.AudioChannels > chosen.AudioChannels) || (f.Height == chosen.Height && f.AudioChannels == chosen.AudioChannels && strings.Contains(f.MimeType, "video/mp4") && !strings.Contains(chosen.MimeType, "video/mp4")) {
			chosen = f
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("youtube: no unencrypted video format is available")
	}
	stream, err := overseasYouTubeURL(ctx, &yt, video, chosen)
	if err != nil {
		return nil, fmt.Errorf("youtube: native signature decoding failed: %w", err)
	}
	m := model.Media{Kind: "video", URL: stream, Width: chosen.Width, Height: chosen.Height, Duration: video.Duration.Seconds(), Headers: overseasHeaders("https://www.youtube.com/")}
	m.Headers["User-Agent"] = youtube.DefaultClient.UserAgent
	m.Headers["Origin"] = "https://youtube.com"
	m.Headers["Sec-Fetch-Mode"] = "navigate"
	if chosen.AudioChannels == 0 {
		if audio == nil {
			return nil, fmt.Errorf("youtube: video-only format has no accessible audio track")
		}
		m.AudioURL, err = overseasYouTubeURL(ctx, &yt, video, audio)
		if err != nil {
			return nil, fmt.Errorf("youtube: audio signature decoding failed: %w", err)
		}
	}
	if strings.Contains(chosen.MimeType, "video/mp4") {
		m.Ext = "mp4"
	} else {
		m.Ext = "webm"
	}
	if len(video.Thumbnails) > 0 {
		m.ThumbnailURL = video.Thumbnails[len(video.Thumbnails)-1].URL
	}
	r := &model.ParseResult{Platform: "youtube", SourceURL: raw, Title: video.Title, Content: video.Description, Author: video.Author}
	overseasAdd(r, m)
	return overseasFinish(r)
}
