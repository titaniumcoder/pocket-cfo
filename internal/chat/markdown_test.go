package chat

import (
	"strings"
	"testing"
)

func TestMarkdownRendersTheLightSubsetAndEscapesTheRest(t *testing.T) {
	in := "Staged **27 lines** for August:\n\n- Groceries `412,80 €`\n- 2 ignored <b>not html</b>\n\n1. first\n2. second\n\n## Next\nApprove them.\nOr say what to change.\n\n```\n{\"id\": \"x\"}\n```"
	got := string(Markdown(in))
	for _, want := range []string{
		"<p>Staged <strong>27 lines</strong> for August:</p>",
		"<ul><li>Groceries <code>412,80 €</code></li><li>2 ignored &lt;b&gt;not html&lt;/b&gt;</li></ul>",
		"<ol><li>first</li><li>second</li></ol>",
		"<p><strong>Next</strong></p>",
		"<p>Approve them.<br>Or say what to change.</p>",
		"<pre><code>{&#34;id&#34;: &#34;x&#34;}</code></pre>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Contains(got, "<b>") {
		t.Error("raw html must stay escaped")
	}
}

func TestMarkdownOfPlainTextIsOneParagraph(t *testing.T) {
	if got := string(Markdown("just words")); got != "<p>just words</p>" {
		t.Errorf("got %q", got)
	}
	if got := string(Markdown("")); got != "" {
		t.Errorf("empty in, got %q", got)
	}
}
