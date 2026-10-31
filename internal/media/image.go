package media

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"multiparse/internal/model"
)

const maxImagePixels int64 = 20_000_000

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func (p *preparation) prepareImage(ctx context.Context, path string) (model.Artifact, error) {
	var empty model.Artifact
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	file, err := os.Open(path)
	if err != nil {
		return empty, err
	}
	config, format, err := image.DecodeConfig(contextReader{ctx, file})
	file.Close()
	if err != nil {
		return empty, errors.New("图片头无效或格式不支持")
	}
	if config.Width < 1 || config.Height < 1 || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > maxImagePixels {
		return empty, errors.New("图片像素数量超过安全限制")
	}
	duration := 0.0
	if format == "gif" {
		duration, err = validateGIF(ctx, path, p.cfg.Media.MaxDurationSeconds)
		if err != nil {
			return empty, err
		}
	}
	file, err = os.Open(path)
	if err != nil {
		return empty, err
	}
	if format == "gif" {
		_, err = gif.DecodeAll(contextReader{ctx, file})
	} else {
		_, _, err = image.Decode(contextReader{ctx, file})
	}
	file.Close()
	if err != nil {
		return empty, errors.New("图片数据损坏")
	}
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	extension := ".jpg"
	if format == "png" {
		extension = ".png"
	}
	if format == "gif" {
		extension = ".gif"
	}
	target := strings.TrimSuffix(path, filepath.Ext(path)) + extension
	if target != path {
		if err = os.Rename(path, target); err != nil {
			return empty, err
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return empty, err
	}
	kind := "image"
	if info.Size() > p.cfg.Media.MaxBytes {
		if p.cfg.Media.OversizeMode != "file" {
			return empty, errors.New("图片大小超过发送限制")
		}
		kind = "file"
	}
	return model.Artifact{Kind: kind, Path: target, Size: info.Size(), Width: config.Width, Height: config.Height, Duration: duration}, nil
}

// Check GIF frame budgets before DecodeAll allocates expanded frame buffers.
func validateGIF(ctx context.Context, path string, maxDuration int) (float64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := bufio.NewReader(contextReader{ctx, file})
	header := make([]byte, 13)
	if _, err = io.ReadFull(reader, header); err != nil {
		return 0, errors.New("GIF头不完整")
	}
	width, height := int64(binary.LittleEndian.Uint16(header[6:8])), int64(binary.LittleEndian.Uint16(header[8:10]))
	if header[10]&0x80 != 0 {
		if _, err = io.CopyN(io.Discard, reader, int64(3*(1<<((header[10]&7)+1)))); err != nil {
			return 0, errors.New("GIF色表不完整")
		}
	}
	var pixels int64
	frames := 0
	duration := 0.0
	delay := 0.0
	subblocks := func() error {
		for {
			size, err := reader.ReadByte()
			if err != nil {
				return err
			}
			if size == 0 {
				return nil
			}
			if _, err = io.CopyN(io.Discard, reader, int64(size)); err != nil {
				return err
			}
		}
	}
	for {
		kind, err := reader.ReadByte()
		if err != nil {
			return 0, errors.New("GIF数据不完整")
		}
		switch kind {
		case 0x3b:
			if frames == 0 {
				return 0, errors.New("GIF没有图像帧")
			}
			if _, err = reader.Peek(1); err != io.EOF {
				return 0, errors.New("GIF含额外尾部数据")
			}
			return duration, nil
		case 0x21:
			label, err := reader.ReadByte()
			if err != nil {
				return 0, err
			}
			if label == 0xf9 {
				control := make([]byte, 6)
				if _, err = io.ReadFull(reader, control); err != nil {
					return 0, err
				}
				if control[0] != 4 || control[5] != 0 {
					return 0, errors.New("GIF控制块无效")
				}
				delay = float64(binary.LittleEndian.Uint16(control[2:4])) / 100
				if delay < 0.02 {
					delay = 0.1
				}
			} else {
				if err = subblocks(); err != nil {
					return 0, errors.New("GIF扩展块损坏")
				}
			}
		case 0x2c:
			descriptor := make([]byte, 9)
			if _, err = io.ReadFull(reader, descriptor); err != nil {
				return 0, err
			}
			x, y := int64(binary.LittleEndian.Uint16(descriptor[0:2])), int64(binary.LittleEndian.Uint16(descriptor[2:4]))
			w, h := int64(binary.LittleEndian.Uint16(descriptor[4:6])), int64(binary.LittleEndian.Uint16(descriptor[6:8]))
			if w < 1 || h < 1 || x+w > width || y+h > height {
				return 0, errors.New("GIF帧尺寸无效")
			}
			frames++
			pixels += w * h
			if frames > 2000 || pixels > maxImagePixels {
				return 0, errors.New("GIF展开帧数量或像素超过安全限制")
			}
			if delay == 0 {
				delay = 0.1
			}
			duration += delay
			delay = 0
			if duration > float64(maxDuration) {
				return 0, errors.New("GIF时长超过限制")
			}
			if descriptor[8]&0x80 != 0 {
				if _, err = io.CopyN(io.Discard, reader, int64(3*(1<<((descriptor[8]&7)+1)))); err != nil {
					return 0, err
				}
			}
			codeSize, err := reader.ReadByte()
			if err != nil || codeSize < 2 || codeSize > 8 {
				return 0, errors.New("GIF压缩参数无效")
			}
			if err = subblocks(); err != nil {
				return 0, errors.New("GIF帧损坏")
			}
		default:
			return 0, errors.New("GIF块类型无效")
		}
	}
}
