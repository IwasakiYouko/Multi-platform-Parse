package media

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type hlsVariant struct {
	uri        string
	attributes map[string]string
	bandwidth  int64
	height     int
}
type hlsCached struct {
	path     string
	finalURL string
}

func hlsAttributes(raw string) (map[string]string, error) {
	result := map[string]string{}
	for raw != "" {
		raw = strings.TrimSpace(raw)
		equal := strings.IndexByte(raw, '=')
		if equal <= 0 {
			return nil, errors.New("HLS属性无效")
		}
		name := raw[:equal]
		raw = raw[equal+1:]
		value := ""
		if strings.HasPrefix(raw, "\"") {
			raw = raw[1:]
			end := strings.IndexByte(raw, '"')
			if end < 0 {
				return nil, errors.New("HLS属性引号不完整")
			}
			value = raw[:end]
			raw = raw[end+1:]
			if raw != "" {
				if raw[0] != ',' {
					return nil, errors.New("HLS属性分隔无效")
				}
				raw = raw[1:]
			}
		} else {
			end := strings.IndexByte(raw, ',')
			if end < 0 {
				value = raw
				raw = ""
			} else {
				value = raw[:end]
				raw = raw[end+1:]
			}
		}
		if _, exists := result[name]; exists {
			return nil, errors.New("HLS属性重复")
		}
		result[name] = value
	}
	return result, nil
}
func resolveHLS(base, reference string) (string, error) {
	if strings.TrimSpace(reference) != reference || strings.ContainsAny(reference, "\x00\r\n\"\\") {
		return "", errors.New("HLS资源URI无效")
	}
	origin, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(reference)
	if err != nil {
		return "", err
	}
	target := origin.ResolveReference(ref)
	if target.Scheme != "http" && target.Scheme != "https" {
		return "", errors.New("HLS仅允许HTTP(S)源资源")
	}
	if origin.Scheme == "https" && target.Scheme != "https" {
		return "", errors.New("HLS拒绝HTTPS降级资源")
	}
	if target.User != nil {
		return "", errors.New("HLS资源禁止认证URL")
	}
	return target.String(), nil
}
func readManifest(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 2<<20 {
		return nil, errors.New("HLS清单超过2MiB限制")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	lines := []string{}
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if strings.ContainsAny(line, "\x00\r") {
			return nil, errors.New("HLS清单含控制字符")
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 || lines[0] != "#EXTM3U" {
		return nil, errors.New("无效HLS清单")
	}
	return lines, nil
}
func (p *preparation) hlsFetch(ctx context.Context, base, reference string, headers map[string]string, extension string, limit int64) (string, string, error) {
	p.hlsResources++
	if p.hlsResources > 20000 {
		return "", "", errors.New("HLS资源数量超过安全限制")
	}
	target, err := resolveHLS(base, reference)
	if err != nil {
		return "", "", err
	}
	cacheKey := extension + "\x00" + target
	if extension != ".m3u8" {
		if cached, ok := p.hlsCache[cacheKey]; ok {
			return cached.path, cached.finalURL, nil
		}
	}
	path, final, err := p.download(ctx, target, headers, limit, extension)
	if err != nil {
		return "", "", err
	}
	if extension != ".m3u8" && extension != ".key" {
		file, err := os.Open(path)
		if err != nil {
			return "", "", err
		}
		prefix := make([]byte, 32)
		n, _ := file.Read(prefix)
		file.Close()
		text := strings.ToLower(strings.TrimSpace(string(prefix[:n])))
		if strings.HasPrefix(text, "#extm3u") || strings.HasPrefix(text, "<html") || strings.HasPrefix(text, "<!doctype") {
			return "", "", errors.New("HLS片段不是二进制媒体")
		}
	}
	if extension != ".m3u8" {
		if p.hlsCache == nil {
			p.hlsCache = make(map[string]hlsCached)
		}
		p.hlsCache[cacheKey] = hlsCached{path: path, finalURL: final}
	}
	return path, final, nil
}
func (p *preparation) manifest(content string) (string, error) {
	if len(content) > 2<<20 {
		return "", errors.New("本地HLS清单过大")
	}
	path := p.next(".m3u8")
	if err := p.claim(int64(len(content))); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		if os.Remove(path) == nil {
			p.unclaim(int64(len(content)))
		}
		return "", err
	}
	return path, nil
}
func (p *preparation) localizeHLS(ctx context.Context, path, base string, headers map[string]string, depth int) (string, error) {
	if depth > 4 {
		return "", errors.New("HLS嵌套层级超过限制")
	}
	lines, err := readManifest(path)
	if err != nil {
		return "", err
	}
	variants := []hlsVariant{}
	audioGroups := map[string]string{}
	for i, line := range lines {
		if strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			a, err := hlsAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			if err != nil {
				return "", err
			}
			if a["TYPE"] == "AUDIO" && a["URI"] != "" && (audioGroups[a["GROUP-ID"]] == "" || a["DEFAULT"] == "YES") {
				audioGroups[a["GROUP-ID"]] = a["URI"]
			}
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			a, err := hlsAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			if err != nil {
				return "", err
			}
			if i+1 >= len(lines) || strings.HasPrefix(lines[i+1], "#") {
				return "", errors.New("HLS变体缺少地址")
			}
			bandwidth, _ := strconv.ParseInt(a["BANDWIDTH"], 10, 64)
			height := 0
			if pieces := strings.Split(a["RESOLUTION"], "x"); len(pieces) == 2 {
				height, _ = strconv.Atoi(pieces[1])
				width, _ := strconv.Atoi(pieces[0])
				if width > 0 && (height <= 0 || width < height) {
					height = width
				}
			}
			variants = append(variants, hlsVariant{uri: lines[i+1], attributes: a, bandwidth: bandwidth, height: height})
		}
	}
	if len(variants) > 0 {
		if len(variants) > 100 {
			return "", errors.New("HLS变体过多")
		}
		quality := p.cfg.Media.Quality
		if quality < 1 {
			quality = 720
		}
		chosen := -1
		for i, v := range variants {
			if v.height > 0 && v.height > quality {
				continue
			}
			if chosen < 0 || v.bandwidth > variants[chosen].bandwidth {
				chosen = i
			}
		}
		if chosen < 0 {
			chosen = 0
			for i, v := range variants {
				if v.height < variants[chosen].height || (v.height == variants[chosen].height && v.bandwidth < variants[chosen].bandwidth) {
					chosen = i
				}
			}
		}
		selected := variants[chosen]
		child, childBase, err := p.hlsFetch(ctx, base, selected.uri, headers, ".m3u8", 2<<20)
		if err != nil {
			return "", err
		}
		localized, err := p.localizeHLS(ctx, child, childBase, headers, depth+1)
		if err != nil {
			return "", err
		}
		if audioURI := audioGroups[selected.attributes["AUDIO"]]; audioURI != "" {
			child, childBase, err = p.hlsFetch(ctx, base, audioURI, headers, ".m3u8", 2<<20)
			if err != nil {
				return "", err
			}
			localAudio, err := p.localizeHLS(ctx, child, childBase, headers, depth+1)
			if err != nil {
				return "", err
			}
			bandwidth := selected.bandwidth
			if bandwidth < 1 {
				bandwidth = 1
			}
			return p.manifest(fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"audio\",DEFAULT=YES,AUTOSELECT=YES,URI=\"%s\"\n#EXT-X-STREAM-INF:BANDWIDTH=%d,AUDIO=\"audio\"\n%s\n", filepath.Base(localAudio), bandwidth, filepath.Base(localized)))
		}
		return localized, nil
	}
	duration := 0.0
	segments := 0
	ended := false
	hasDuration := false
	pendingRange := ""
	previousResource := ""
	previousRangeEnd := int64(0)
	previousHadRange := false
	output := []string{"#EXTM3U"}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "#") {
			if !hasDuration {
				return "", errors.New("HLS片段缺少时长")
			}
			segments++
			if segments > 10000 {
				return "", errors.New("HLS片段数量超过限制")
			}
			segment, _, err := p.hlsFetch(ctx, base, line, headers, ".ts", p.hardLimit())
			if err != nil {
				return "", err
			}
			resource, err := resolveHLS(base, line)
			if err != nil {
				return "", err
			}
			if pendingRange != "" {
				pieces := strings.Split(pendingRange, "@")
				length, _ := strconv.ParseInt(pieces[0], 10, 64)
				offset := int64(0)
				if len(pieces) == 2 {
					offset, _ = strconv.ParseInt(pieces[1], 10, 64)
				} else {
					if !previousHadRange || previousResource != resource {
						return "", errors.New("HLS隐式字节范围缺少相同前序资源")
					}
					offset = previousRangeEnd
				}
				info, err := os.Stat(segment)
				if err != nil {
					return "", err
				}
				if offset > info.Size() || length > info.Size()-offset {
					return "", errors.New("HLS字节范围超出资源大小")
				}
				output = append(output, fmt.Sprintf("#EXT-X-BYTERANGE:%d@%d", length, offset))
				previousRangeEnd = offset + length
				previousHadRange = true
			} else {
				previousHadRange = false
			}
			pendingRange = ""
			previousResource = resource
			output = append(output, filepath.Base(segment))
			hasDuration = false
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			raw := strings.SplitN(strings.TrimPrefix(line, "#EXTINF:"), ",", 2)[0]
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
				return "", errors.New("HLS片段时长无效")
			}
			duration += value
			if duration > float64(p.cfg.Media.MaxDurationSeconds) {
				return "", errors.New("HLS总时长超过限制")
			}
			hasDuration = true
			output = append(output, "#EXTINF:"+raw+",")
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			a, err := hlsAttributes(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			if err != nil {
				return "", err
			}
			if a["METHOD"] == "NONE" {
				output = append(output, "#EXT-X-KEY:METHOD=NONE")
				continue
			}
			if a["METHOD"] != "AES-128" || (a["KEYFORMAT"] != "" && a["KEYFORMAT"] != "identity") || a["URI"] == "" {
				return "", errors.New("HLS使用不支持的加密或DRM（仅支持AES-128）")
			}
			key, _, err := p.hlsFetch(ctx, base, a["URI"], headers, ".key", 16)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(key)
			if err != nil || info.Size() != 16 {
				return "", errors.New("HLS AES密钥必须为16字节")
			}
			local := "#EXT-X-KEY:METHOD=AES-128,URI=\"" + filepath.Base(key) + "\""
			if iv := a["IV"]; iv != "" {
				if !strings.HasPrefix(iv, "0x") || len(iv) > 34 || len(iv) < 3 {
					return "", errors.New("HLS IV无效")
				}
				for _, c := range iv[2:] {
					if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
						return "", errors.New("HLS IV无效")
					}
				}
				local += ",IV=" + iv
			}
			output = append(output, local)
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			a, err := hlsAttributes(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if err != nil {
				return "", err
			}
			if a["URI"] == "" {
				return "", errors.New("HLS初始化片段缺少URI")
			}
			init, _, err := p.hlsFetch(ctx, base, a["URI"], headers, ".mp4", p.hardLimit())
			if err != nil {
				return "", err
			}
			tag := "#EXT-X-MAP:URI=\"" + filepath.Base(init) + "\""
			if a["BYTERANGE"] != "" {
				if !validByteRange(a["BYTERANGE"]) {
					return "", errors.New("HLS字节范围无效")
				}
				tag += ",BYTERANGE=\"" + a["BYTERANGE"] + "\""
			}
			output = append(output, tag)
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			raw := strings.TrimPrefix(line, "#EXT-X-BYTERANGE:")
			if pendingRange != "" || !validByteRange(raw) {
				return "", errors.New("HLS字节范围无效")
			}
			pendingRange = raw
		case line == "#EXT-X-ENDLIST":
			ended = true
			output = append(output, line)
		case strings.HasPrefix(line, "#EXT-X-VERSION:"), strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"), strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), strings.HasPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			_, raw, _ := strings.Cut(line, ":")
			number, err := strconv.ParseUint(raw, 10, 63)
			if err != nil || number > math.MaxInt64-10000 {
				return "", errors.New("HLS数值标签无效")
			}
			output = append(output, line)
		case strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE:"):
			if line != "#EXT-X-PLAYLIST-TYPE:VOD" && line != "#EXT-X-PLAYLIST-TYPE:EVENT" {
				return "", errors.New("HLS清单类型无效")
			}
			output = append(output, line)
		case line == "#EXT-X-DISCONTINUITY", line == "#EXT-X-GAP", line == "#EXT-X-INDEPENDENT-SEGMENTS":
			output = append(output, line)
		case strings.HasPrefix(line, "#EXT-X-PART"), strings.HasPrefix(line, "#EXT-X-PRELOAD-HINT"), strings.HasPrefix(line, "#EXT-X-SESSION-KEY"):
			return "", errors.New("不支持低延迟或会话加密HLS")
		default:
			// Unknown tags are omitted, never copied with an unvalidated URI.
		}
	}
	if !ended || segments == 0 || hasDuration || pendingRange != "" {
		return "", errors.New("仅支持完整、已结束的点播HLS，直播流不受支持")
	}
	return p.manifest(strings.Join(output, "\n") + "\n")
}
func validByteRange(raw string) bool {
	pieces := strings.Split(raw, "@")
	if len(pieces) > 2 {
		return false
	}
	length, err := strconv.ParseUint(pieces[0], 10, 63)
	if err != nil || length == 0 {
		return false
	}
	if len(pieces) == 2 {
		offset, err := strconv.ParseUint(pieces[1], 10, 63)
		if err != nil || offset > math.MaxInt64-length {
			return false
		}
	}
	return true
}
