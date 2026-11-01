package media

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"multiparse/internal/extractor"
	"multiparse/internal/model"
)

type extractionDownloader func(context.Context, *http.Client, model.Extraction, string, string, string, int64) ([]string, error)

func (p *preparation) prepareExtraction(ctx context.Context, result model.ParseResult) ([]model.Artifact, error) {
	paths, err := p.stageExtraction(ctx, result, extractor.Download)
	if err != nil {
		return nil, err
	}
	return p.prepareExtracted(ctx, result, paths)
}

func (p *preparation) prepareExtracted(ctx context.Context, result model.ParseResult, paths []string) ([]model.Artifact, error) {
	item := model.Media{Kind: "video"}
	if len(result.Media) > 0 {
		item = result.Media[0]
	}
	// Metadata URLs are references, not another download request.
	item.URL, item.AudioURL, item.VideoURL, item.BackupURLs = "", "", "", nil
	headers := mediaHeaders(result, item)
	artifacts := make([]model.Artifact, 0, len(paths))
	for _, path := range paths {
		a, err := p.prepareLocal(ctx, item, path, headers)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	if item.ThumbnailURL != "" && len(artifacts) > 0 && artifacts[0].ThumbPath != "" {
		cover, err := p.optionalExtractionCover(ctx, item.ThumbnailURL, headers)
		if err != nil {
			return nil, err
		}
		if cover.Path != "" {
			if err = p.discard(artifacts[0].ThumbPath); err != nil {
				return nil, err
			}
			artifacts[0].ThumbPath = cover.Path
		}
	}
	return artifacts, nil
}

// Reserve the backend's complete working set before it can spawn a child process.
func (p *preparation) stageExtraction(ctx context.Context, result model.ParseResult, download extractionDownloader) (_ []string, err error) {
	if result.Extraction == nil || result.Extraction.Engine != extractor.Engine {
		return nil, errors.New("不支持的媒体提取引擎")
	}
	if result.Platform != "youtube" && result.Platform != "facebook" && result.Platform != "snapchat" {
		return nil, errors.New("不支持的媒体提取平台")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	p.manager.mu.Lock()
	available := p.cfg.Media.MaxDiskBytes - p.manager.used
	p.manager.mu.Unlock()
	limit := min(3*p.hardLimit(), available)
	if limit < 1024 {
		return nil, errors.New("媒体缓存没有足够空间预留提取器工作集")
	}
	if err = p.claim(limit); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(p.dir, ".extract-")
	if err != nil {
		p.unclaim(limit)
		return nil, err
	}
	var moved []string
	var total int64
	success := false
	defer func() {
		cleanupErr := os.RemoveAll(dir)
		if !success || cleanupErr != nil {
			for _, path := range moved {
				cleanupErr = errors.Join(cleanupErr, os.Remove(path))
			}
			if cleanupErr == nil {
				p.unclaim(limit)
			}
		} else {
			p.unclaim(limit - total)
		}
		err = errors.Join(err, cleanupErr)
	}()
	cookie := ""
	if result.Platform == "youtube" {
		cookie = p.cfg.Platforms[result.Platform].Cookie
	}
	paths, err := download(ctx, p.client, *result.Extraction, result.Platform, cookie, dir, limit)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	actual, err := treeSize(dir)
	if err != nil {
		return nil, err
	}
	if actual > limit || len(paths) == 0 || len(paths) > p.cfg.Media.MaxImages+16 {
		return nil, errors.New("提取器输出超过工作集或数量限制")
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil || rel == "." || !filepath.IsLocal(rel) || seen[filepath.Clean(path)] {
			return nil, errors.New("提取器返回越界或重复路径")
		}
		if err = p.validateLocal(path); err != nil {
			return nil, err
		}
		seen[filepath.Clean(path)] = true
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		total += info.Size()
	}
	for _, path := range paths {
		target := p.next(".bin")
		if err = os.Rename(path, target); err != nil {
			return nil, err
		}
		moved = append(moved, target)
	}
	success = true
	return moved, nil
}

// Metadata covers are optional; a valid locally generated video thumbnail is not.
func (p *preparation) optionalExtractionCover(ctx context.Context, raw string, headers map[string]string) (model.Artifact, error) {
	before, beforeBytes := p.sequence, p.bytes
	cover, prepareErr := p.prepareItem(ctx, model.Media{Kind: "image", URL: raw}, headers)
	if prepareErr == nil && ctx.Err() == nil && cover.Kind == "image" && cover.ThumbPath == "" && cover.Size <= 4<<20 && filepath.Ext(cover.Path) != ".gif" {
		return cover, nil
	}
	entries, cleanupErr := os.ReadDir(p.dir)
	for _, entry := range entries {
		stem, _, _ := strings.Cut(entry.Name(), ".")
		sequence, parseErr := strconv.Atoi(stem)
		if parseErr == nil && sequence > before {
			cleanupErr = errors.Join(cleanupErr, p.discard(filepath.Join(p.dir, entry.Name())))
		}
	}
	if cleanupErr != nil {
		return model.Artifact{}, errors.Join(prepareErr, ctx.Err(), cleanupErr)
	}
	if p.bytes != beforeBytes {
		return model.Artifact{}, errors.Join(prepareErr, ctx.Err(), errMediaAccountingOverflow)
	}
	if err := ctx.Err(); err != nil {
		return model.Artifact{}, err
	}
	var pathErr *os.PathError
	var linkErr *os.LinkError
	var syscallErr *os.SyscallError
	if errors.As(prepareErr, &pathErr) || errors.As(prepareErr, &linkErr) || errors.As(prepareErr, &syscallErr) || errors.Is(prepareErr, errInvalidReservation) || errors.Is(prepareErr, errMediaDiskLimit) || errors.Is(prepareErr, errMediaAccountingOverflow) {
		return model.Artifact{}, prepareErr
	}
	return model.Artifact{}, nil
}
