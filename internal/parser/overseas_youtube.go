package parser

import (
	"context"
	"net/http"

	"multiparse/internal/extractor"
	"multiparse/internal/model"
)

func init() { Register("youtube", overseasYouTube) }

func overseasYouTube(ctx context.Context, client *http.Client, raw, cookie string) (*model.ParseResult, error) {
	return extractor.Resolve(ctx, client, "youtube", raw, cookie)
}
