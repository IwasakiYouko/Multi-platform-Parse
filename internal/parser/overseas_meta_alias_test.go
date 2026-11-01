package parser

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestMigratedMetaRegisteredAliasesAndHTTPInputs(t *testing.T) {
	client := &http.Client{Transport: overseasTestTransport(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("post canonicalization unexpectedly fetched %s", r.URL)
	})}
	for _, test := range []struct{ platform, input, want string }{
		{"instagram", "http://www.instagram.com/p/AbC/", "https://www.instagram.com/p/AbC/"},
		{"instagram", "https://instagr.am/p/AbC/", "https://www.instagram.com/p/AbC/"},
		{"instagram", "http://www.instagr.am/reel/AbC/", "https://www.instagram.com/reel/AbC/"},
		{"threads", "https://www.threads.net/@a/post/AbC/", "https://www.threads.com/@a/post/AbC/"},
		{"threads", "http://threads.net/@a/post/AbC/", "https://www.threads.com/@a/post/AbC/"},
		{"threads", "http://www.threads.com/@a/post/AbC/", "https://www.threads.com/@a/post/AbC/"},
	} {
		got, err := overseasMetaShare(context.Background(), client, test.platform, test.input)
		if err != nil || got != test.want {
			t.Fatalf("registered input rejected or misrouted: %s => %s (%v)", test.input, got, err)
		}
	}
	for _, input := range []string{"ftp://www.instagram.com/p/AbC/", "https://instagram.com.evil/p/AbC/", "https://user:secret@www.instagram.com/p/AbC/", "https://www.instagram.com:444/p/AbC/"} {
		if _, err := overseasMetaShare(context.Background(), client, "instagram", input); err == nil {
			t.Fatalf("unsafe alias accepted: %s", input)
		}
	}
}
