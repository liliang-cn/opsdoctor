package cite

import "testing"

// A model that lists its own sources, in any markdown form, must not get a
// second list appended underneath.
func TestFooterSkipsAnAnswerThatListsItsOwnSources(t *testing.T) {
	src := "linbit-blog-kb/kb.linbit.com/drbd/backing-up-drbd-from-the-secondary.md"
	cited := "Snapshot created [" + Label(src) + "].\n\n"
	for _, heading := range []string{"Sources:", "**Sources**", "## Sources", "Sources", "来源：", "**参考来源**"} {
		if got := Footer(cited+heading+"\n- ["+Label(src)+"] "+src, []string{src}); got != "" {
			t.Errorf("heading %q: footer appended anyway:\n%s", heading, got)
		}
	}
	// An answer that only mentions the word still gets its footer.
	if got := Footer(cited+"These sources agree.", []string{src}); got == "" {
		t.Error("a passing mention of 'sources' suppressed the footer")
	}
}
