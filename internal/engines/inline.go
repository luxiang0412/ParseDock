package engines

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// liftInline splits one piece of Markdown inline content into the plain text the IR
// stores in `text` and the rich text it stores in `rich_text`. rich is empty when the
// input carries no markup (stripping changes nothing), so formatting-free content does
// not store the same string twice.
func liftInline(raw string) (plain, rich string) {
	plain = stripInline(raw)
	if plain == raw {
		return plain, ""
	}
	return plain, raw
}

// stripInline removes CommonMark/GFM inline markup and returns the words a reader sees:
// emphasis, strong, strikethrough and code-span delimiters are dropped, links and images
// become their text, backslash escapes and autolinks are resolved, and <br> becomes a
// line break. Anything that does not form valid markup stays literal, which is what a
// Markdown renderer would show, so an unmatched "*" or "[" is never lost.
//
// It implements the subset of CommonMark 0.31 section 6 that engines actually emit; it is
// not a full parser (no reference links, no entity decoding beyond what engines use).
func stripInline(s string) string {
	if !strings.ContainsAny(s, "\\`*_~[]<!&") {
		return s
	}
	toks := tokenizeInline([]rune(s))
	matchDelimiters(toks)
	var b strings.Builder
	for _, t := range toks {
		if t.delim != 0 {
			b.WriteString(strings.Repeat(string(t.delim), t.n))
			continue
		}
		b.WriteString(t.text)
	}
	return b.String()
}

type inlineTok struct {
	text      string
	delim     rune // '*', '_' or '~' for a delimiter run; 0 for text
	n         int  // remaining delimiter characters
	orig      int  // original run length (rule of three)
	canOpen   bool
	canClose  bool
	prevPunct bool
	nextPunct bool
}

var inlineFormatTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true, "u": true, "s": true, "del": true,
	"strike": true, "sup": true, "sub": true, "code": true, "mark": true, "span": true, "ins": true,
}

var htmlEntities = map[string]string{
	"&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": `"`, "&#39;": "'", "&apos;": "'", "&nbsp;": " ",
}

func tokenizeInline(rs []rune) []inlineTok {
	var toks []inlineTok
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			toks = append(toks, inlineTok{text: text.String()})
			text.Reset()
		}
	}
	at := func(i int) rune {
		if i < 0 || i >= len(rs) {
			return ' '
		}
		return rs[i]
	}
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '\\' && i+1 < len(rs) && isASCIIPunctRune(rs[i+1]):
			text.WriteRune(rs[i+1])
			i += 2
			continue
		case r == '\\' && i+1 == len(rs):
			// A trailing backslash is a hard line break marker; it carries no text.
			i++
			continue
		case r == '`':
			if body, next, ok := codeSpan(rs, i); ok {
				text.WriteString(body)
				i = next
				continue
			}
			// An unmatched backtick run is literal, all of it.
			j := i
			for j < len(rs) && rs[j] == '`' {
				j++
			}
			text.WriteString(string(rs[i:j]))
			i = j
			continue
		case r == '!' && at(i+1) == '[':
			if label, next, ok := inlineLink(rs, i+1); ok {
				text.WriteString(stripInline(label))
				i = next
				continue
			}
		case r == '[':
			if label, next, ok := inlineLink(rs, i); ok {
				text.WriteString(stripInline(label))
				i = next
				continue
			}
		case r == '<':
			if repl, next, ok := inlineAngle(rs, i); ok {
				text.WriteString(repl)
				i = next
				continue
			}
		case r == '&':
			if repl, n, ok := inlineEntity(rs, i); ok {
				text.WriteString(repl)
				i += n
				continue
			}
		case r == '*' || r == '_' || r == '~':
			j := i
			for j < len(rs) && rs[j] == r {
				j++
			}
			n := j - i
			prev, next := at(i-1), at(j)
			left := !unicode.IsSpace(next) && (!isPunctRune(next) || unicode.IsSpace(prev) || isPunctRune(prev))
			right := !unicode.IsSpace(prev) && (!isPunctRune(prev) || unicode.IsSpace(next) || isPunctRune(next))
			tok := inlineTok{delim: r, n: n, orig: n, prevPunct: isPunctRune(prev), nextPunct: isPunctRune(next)}
			switch r {
			case '_':
				tok.canOpen = left && (!right || tok.prevPunct)
				tok.canClose = right && (!left || tok.nextPunct)
			default:
				tok.canOpen, tok.canClose = left, right
			}
			if r == '~' && n > 2 {
				// GFM: three or more tildes never form strikethrough.
				tok.canOpen, tok.canClose = false, false
			}
			flush()
			toks = append(toks, tok)
			i = j
			continue
		}
		text.WriteRune(r)
		i++
	}
	flush()
	return toks
}

// matchDelimiters pairs emphasis/strikethrough delimiter runs (CommonMark 6.2 "process
// emphasis", simplified to the cases engines emit). Matched characters are consumed;
// whatever remains is printed literally.
func matchDelimiters(toks []inlineTok) {
	for c := range toks {
		closer := &toks[c]
		if closer.delim == 0 || !closer.canClose {
			continue
		}
		for closer.n > 0 {
			o := -1
			for j := c - 1; j >= 0; j-- {
				op := &toks[j]
				if op.delim != closer.delim || op.n == 0 || !op.canOpen {
					continue
				}
				if closer.delim == '~' && op.n != closer.n {
					continue
				}
				// Rule of three: a run that can both open and close does not pair with
				// one whose length makes the sum a multiple of three.
				if closer.delim != '~' && (op.canClose || closer.canOpen) &&
					(op.orig+closer.orig)%3 == 0 && (op.orig%3 != 0 || closer.orig%3 != 0) {
					continue
				}
				o = j
				break
			}
			if o < 0 {
				break
			}
			op := &toks[o]
			use := 1
			if closer.delim == '~' {
				use = closer.n
			} else if op.n >= 2 && closer.n >= 2 {
				use = 2
			}
			op.n -= use
			closer.n -= use
			// Delimiters strictly between a matched pair can no longer match outward.
			for k := o + 1; k < c; k++ {
				if toks[k].delim != 0 {
					toks[k].canOpen, toks[k].canClose = false, false
				}
			}
		}
	}
}

// codeSpan parses a backtick code span starting at i. It returns the literal content
// (CommonMark: one leading and trailing space stripped when both exist, line endings as
// spaces) and the index after the closing run.
func codeSpan(rs []rune, i int) (string, int, bool) {
	n := 0
	for i+n < len(rs) && rs[i+n] == '`' {
		n++
	}
	for j := i + n; j < len(rs); {
		if rs[j] != '`' {
			j++
			continue
		}
		k := j
		for k < len(rs) && rs[k] == '`' {
			k++
		}
		if k-j == n {
			body := strings.ReplaceAll(string(rs[i+n:j]), "\n", " ")
			if len(body) >= 2 && body[0] == ' ' && body[len(body)-1] == ' ' && strings.Trim(body, " ") != "" {
				body = body[1 : len(body)-1]
			}
			return body, k, true
		}
		j = k
	}
	return "", 0, false
}

// inlineLink parses "[label](destination "title")" starting at the '[' at index i and
// returns the raw label. Brackets may nest; escapes and code spans inside the label are
// respected. Reference-style links are not resolved (no definitions are available).
func inlineLink(rs []rune, i int) (string, int, bool) {
	depth := 0
	j := i
	for ; j < len(rs); j++ {
		switch rs[j] {
		case '\\':
			j++
			continue
		case '`':
			if _, next, ok := codeSpan(rs, j); ok {
				j = next - 1
				continue
			}
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if j >= len(rs) || j+1 >= len(rs) || rs[j+1] != '(' {
		return "", 0, false
	}
	label := string(rs[i+1 : j])
	// Destination: balanced parentheses, or <...>; then an optional title.
	k := j + 2
	parens := 1
	inAngle := k < len(rs) && rs[k] == '<'
	for ; k < len(rs); k++ {
		r := rs[k]
		if r == '\\' {
			k++
			continue
		}
		if inAngle {
			if r == '>' {
				inAngle = false
			}
			continue
		}
		if r == '"' || r == '\'' {
			// Title: skip to the matching quote.
			q := r
			for k++; k < len(rs) && rs[k] != q; k++ {
				if rs[k] == '\\' {
					k++
				}
			}
			continue
		}
		switch r {
		case '(':
			parens++
		case ')':
			parens--
			if parens == 0 {
				return label, k + 1, true
			}
		case '\n':
			return "", 0, false
		}
	}
	return "", 0, false
}

// inlineAngle handles what may follow '<': an autolink (<https://x>, <a@b.c>), a line
// break tag (<br>, <br/>), or an inline formatting tag that carries no text of its own.
// Any other HTML stays literal.
func inlineAngle(rs []rune, i int) (string, int, bool) {
	end := -1
	for j := i + 1; j < len(rs) && j < i+2048; j++ {
		if rs[j] == '>' {
			end = j
			break
		}
		if rs[j] == '<' || rs[j] == '\n' {
			return "", 0, false
		}
	}
	if end < 0 {
		return "", 0, false
	}
	inner := string(rs[i+1 : end])
	if inner == "" {
		return "", 0, false
	}
	if !strings.ContainsAny(inner, " \t") {
		if p := strings.Index(inner, ":"); p > 1 && isScheme(inner[:p]) {
			return inner, end + 1, true
		}
		if at := strings.Index(inner, "@"); at > 0 && strings.Contains(inner[at:], ".") && !strings.ContainsAny(inner, "/\\") {
			return inner, end + 1, true
		}
	}
	tag := strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(inner), "/")))
	if f := strings.Fields(tag); len(f) > 0 {
		tag = f[0]
	}
	if tag == "br" {
		return "\n", end + 1, true
	}
	if inlineFormatTags[tag] {
		return "", end + 1, true
	}
	return "", 0, false
}

func inlineEntity(rs []rune, i int) (string, int, bool) {
	for name, repl := range htmlEntities {
		n := utf8.RuneCountInString(name)
		if i+n <= len(rs) && string(rs[i:i+n]) == name {
			return repl, n, true
		}
	}
	return "", 0, false
}

func isScheme(s string) bool {
	if len(s) < 2 || len(s) > 32 {
		return false
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && (r >= '0' && r <= '9' || r == '+' || r == '.' || r == '-')
		if !ok {
			return false
		}
	}
	return true
}

func isASCIIPunctRune(r rune) bool {
	return r < utf8.RuneSelf && (unicode.IsPunct(r) || unicode.IsSymbol(r))
}

// isPunctRune is CommonMark's "Unicode punctuation character" (P and S categories).
func isPunctRune(r rune) bool {
	return unicode.IsPunct(r) || unicode.IsSymbol(r)
}
