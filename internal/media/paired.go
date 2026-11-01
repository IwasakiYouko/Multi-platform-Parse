package media

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"multiparse/internal/model"
)

func mediaHeaders(result model.ParseResult, item model.Media) map[string]string {
	headers := make(map[string]string, len(result.Headers)+len(item.Headers)+2)
	headers["User-Agent"] = userAgent
	headers["Referer"] = result.SourceURL
	for k, v := range result.Headers {
		headers[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range item.Headers {
		headers[http.CanonicalHeaderKey(k)] = v
	}
	return headers
}

func (p *preparation) prepareLivePhoto(ctx context.Context, item model.Media, headers map[string]string) (_ model.Artifact, err error) {
	if item.URL == "" || item.VideoURL == "" || item.AudioURL != "" {
		return model.Artifact{}, errors.New("实况照片必须包含图片和视频，且不能引用独立音频")
	}
	before := p.sequence
	// A pair is atomic even for callers preparing an item without Manager.Prepare.
	defer func() {
		if err == nil {
			return
		}
		entries, readErr := os.ReadDir(p.dir)
		err = errors.Join(err, readErr)
		for _, entry := range entries {
			stem, _, _ := strings.Cut(entry.Name(), ".")
			sequence, parseErr := strconv.Atoi(stem)
			if parseErr == nil && sequence > before {
				err = errors.Join(err, p.discard(filepath.Join(p.dir, entry.Name())))
			}
		}
	}()
	photoItem := item
	photoItem.Kind, photoItem.VideoURL = "image", ""
	photo, err := p.prepareItem(ctx, photoItem, headers)
	if err != nil {
		return model.Artifact{}, err
	}
	if photo.Width == 0 || photo.ThumbPath != "" || (photo.Kind != "image" && photo.Kind != "file") {
		return model.Artifact{}, errors.New("实况照片图片地址实际返回非图片媒体")
	}
	video, err := p.prepareItem(ctx, model.Media{Kind: "video", URL: item.VideoURL}, headers)
	if err != nil {
		return model.Artifact{}, err
	}
	if video.ThumbPath == "" || (video.Kind != "video" && video.Kind != "file") {
		return model.Artifact{}, errors.New("实况照片伴随媒体不含视频")
	}
	if err = ctx.Err(); err != nil {
		return model.Artifact{}, err
	}
	photo.Kind = "livephoto"
	photo.VideoPath, photo.VideoSize = video.Path, video.Size
	photo.ThumbPath, photo.Duration = video.ThumbPath, video.Duration
	return photo, nil
}

func (p *preparation) validateLocal(path string) error {
	root, err := filepath.Abs(p.dir)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return errors.New("提取媒体路径必须是绝对路径")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return errors.New("提取媒体路径越界")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != filepath.Clean(path) {
		return errors.New("提取媒体路径包含符号链接")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > p.hardLimit() {
		return errors.New("提取媒体不是受限普通文件")
	}
	return nil
}
