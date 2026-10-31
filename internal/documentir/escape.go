package documentir

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The Markdown view renders plain text that came from documents, so characters the
// source used literally must not turn into markup: "*" in "5 *estimated", a line that
// starts with "1." or "#", "<b>" in a sentence about HTML. The escaping is deliberately
// minimal - only where CommonMark/GFM would otherwise reinterpret the character - so the
// Markdown source stays readable for people and LLMs (an identifier like on_time_pct is
// left alone because an intraword underscore is never emphasis).

// EscapeMarkdownInline escapes plain text for use inside CommonMark inline content. It
// is for adapters that assemble rich_text from formatted runs: the run text is literal,
// only the markup around it is Markdown.
func EscapeMarkdownInline(s string) string { return escapeInline(s) }

// escapeInline escapes characters that could start inline markup.
func escapeInline(s string) string {
	if s == "" || !strings.ContainsAny(s, "\\*_`~[]<&") {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s) + 8)
	at := func(i int) rune {
		if i < 0 || i >= len(rs) {
			return ' '
		}
		return rs[i]
	}
	for i, r := range rs {
		prev, next := at(i-1), at(i+1)
		switch r {
		case '\\':
			// A backslash is literal unless it precedes ASCII punctuation.
			if isASCIIPunct(next) {
				b.WriteByte('\\')
			}
		case '`':
			b.WriteByte('\\')
		case '*', '~':
			// Only a delimiter that touches a non-space character can open or close.
			if !unicode.IsSpace(prev) || !unicode.IsSpace(next) {
				b.WriteByte('\\')
			}
		case '_':
			// Intraword underscores are never emphasis (CommonMark 6.2).
			if !(isWordRune(prev) && isWordRune(next)) && (!unicode.IsSpace(prev) || !unicode.IsSpace(next)) {
				b.WriteByte('\\')
			}
		case ']':
			// Without "](", "][" or "]:" no link or reference can form.
			if next == '(' || next == '[' || next == ':' {
				b.WriteByte('\\')
			}
		case '[':
			// "[" is harmless on its own; it only matters for "![...](...)".
			if prev == '!' {
				b.WriteByte('\\')
			}
		case '<':
			// "<x", "</", "<!" and "<?" start raw HTML or an autolink.
			if unicode.IsLetter(next) || next == '/' || next == '!' || next == '?' {
				b.WriteByte('\\')
			}
		case '&':
			if entityRe.MatchString(string(rs[i:min(len(rs), i+34)])) {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

var (
	entityRe = regexp.MustCompile(`^&(#[0-9]{1,7}|#[xX][0-9A-Fa-f]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});`)
	// Block constructs a plain line could accidentally start.
	atxRe       = regexp.MustCompile(`^#{1,6}(\s|$)`)
	bulletRe    = regexp.MustCompile(`^[-+*](\s|$)`)
	orderedRe   = regexp.MustCompile(`^([0-9]{1,9})([.)])(\s|$)`)
	breakRe     = regexp.MustCompile(`^([-*_=])(\s*[-*_=]){2,}\s*$|^=+\s*$`)
	delimiterRe = regexp.MustCompile(`^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$`)
)

// escapeLine escapes one line of plain text that starts a block (a paragraph line, a
// list item body): inline characters plus anything that would open a heading, quote,
// list, thematic break, setext underline or table.
func escapeLine(s string) string {
	s = strings.TrimLeft(s, " \t") // four leading spaces would start an indented code block
	if s == "" {
		return s
	}
	prefix := ""
	switch {
	case atxRe.MatchString(s), strings.HasPrefix(s, ">"):
		prefix, s = `\`+s[:1], s[1:]
	case breakRe.MatchString(s), bulletRe.MatchString(s):
		// "-", "+", "*", "=" and "_" handled here; "*" and "_" would also be escaped below,
		// so emit the escaped first character directly.
		prefix, s = `\`+s[:1], s[1:]
	default:
		if m := orderedRe.FindStringSubmatch(s); m != nil {
			prefix, s = m[1]+`\`+m[2], s[len(m[1])+len(m[2]):]
		}
	}
	out := prefix + escapeInline(s)
	if strings.Contains(out, "|") && delimiterRe.MatchString(strings.TrimSpace(prefix+s)) {
		// A line that looks like a GFM delimiter row would turn the paragraph into a table.
		out = escapePipes(out)
	}
	return out
}

// escapeLinkText escapes the text of an image alt / link label.
func escapeLinkText(s string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(s)
}

func isASCIIPunct(r rune) bool {
	return r < utf8.RuneSelf && unicode.IsPunct(r) || strings.ContainsRune("$+<=>^`|~", r)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
