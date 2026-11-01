package parser

import (
	"context"
	"net/http"

	"multiparse/internal/extractor"
	"multiparse/internal/model"
)

func init() { Register("snapchat", overseasSnapchat) }

func overseasSnapchat(ctx context.Context, client *http.Client, raw, _ string) (*model.ParseResult, error) {
	return extractor.Resolve(ctx, client, "snapchat", raw, "")
}
