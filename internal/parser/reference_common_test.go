package parser

import (
	"errors"
	"strings"
	"testing"

	"multiparse/internal/model"
)

func TestReferenceHTMLRetainsLinksCodeAndImagePositions(t *testing.T) {
	input := `<h2>Heading</h2><p>before <a href="../target?q=1">label</a></p><img src="/preview.jpg" data-src="/original.jpg"><blockquote>quote</blockquote><pre><code class="language-go">if a &lt; b { return }</code></pre><p><del>removed</del> after</p><script>secretScript()</script>`
	plain, markdown := referenceHTML(input, "https://example.com/posts/one")
	for _, fragment := range []string{"## Heading", "https://example.com/target?q=1", "https://example.com/original.jpg", "```", "if a < b", "> quote", "~~removed~~"} {
		if !strings.Contains(markdown, fragment) {
			t.Fatalf("lost richtext semantic %q: %s", fragment, markdown)
		}
	}
	if strings.Contains(markdown, "preview.jpg") || strings.Contains(markdown, "secretScript") || strings.Contains(plain, "secretScript") {
		t.Fatalf("unsafe/placeholder content retained: %s", markdown)
	}
	if !(strings.Index(markdown, "before") < strings.Index(markdown, "original.jpg") && strings.Index(markdown, "original.jpg") < strings.Index(markdown, "after")) {
		t.Fatalf("image position changed: %s", markdown)
	}
}
func TestReferenceHTMLRejectsExecutableLinksButKeepsEmbedTargets(t *testing.T) {
	_, markdown := referenceHTML(`<p><a href="javascript:alert(1)">label</a></p><iframe src="https://video.example/embed/1"></iframe>`, "https://example.com/")
	if strings.Contains(markdown, "javascript:") || !strings.Contains(markdown, "https://video.example/embed/1") || !strings.Contains(markdown, "label") {
		t.Fatalf("link semantics unsafe or missing: %s", markdown)
	}
}
func TestReferenceFallbackStopsOnlyClassifiedPermanentFailure(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		calls := 0
		transient := errors.New("transport failed")
		var first error = transient
		if permanent {
			first = ContentUnavailableError{Reason: "deleted"}
		}
		result, err := referenceFallback([]referenceStage{
			{Name: "first", Run: func() (*model.ParseResult, error) { return nil, first }},
			{Name: "second", Run: func() (*model.ParseResult, error) { calls++; return &model.ParseResult{Content: "available"}, nil }},
		}, true)
		if permanent {
			if !referenceUnavailable(err) || result != nil || calls != 0 {
				t.Fatalf("deleted content retried: result=%+v err=%v calls=%d", result, err, calls)
			}
		} else if err != nil || result.Content != "available" || calls != 1 || len(result.FallbackErrors) != 1 || result.FallbackErrors[0].Name != "first" {
			t.Fatalf("transient fallback failed or lost attempts: %+v %v", result, err)
		}
	}
}
