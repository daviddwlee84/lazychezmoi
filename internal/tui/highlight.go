package tui

import (
	"bytes"
	"charm.land/lipgloss/v2"
	"github.com/daviddwlee84/lazychezmoi/internal/search"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

// Strip all terminal controls supplied by file contents, paths and tool errors.
// Only the application and trusted syntax formatter may emit ANSI sequences.
func sanitize(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func highlightMatch(line string, spans []search.Span, color bool) string {
	spans = append([]search.Span{}, spans...)
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	var b strings.Builder
	position := 0
	clean := func(s string) string { return strings.ReplaceAll(sanitize(s), "\t", "    ") }
	for _, span := range spans {
		if span.Start < position || span.Start < 0 || span.End < span.Start || span.End > len(line) {
			continue
		}
		b.WriteString(clean(line[position:span.Start]))
		match := clean(line[span.Start:span.End])
		if color {
			match = lipgloss.NewStyle().Background(lipgloss.Color("3")).Foreground(lipgloss.Color("0")).Render(match)
		}
		b.WriteString(match)
		position = span.End
	}
	b.WriteString(clean(line[position:]))
	return b.String()
}

func singleLine(s string) string {
	return strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(sanitize(s))
}

func lexerFor(e chezmoi.Entry, view, content string) chroma.Lexer {
	if view == "diff" {
		return lexers.Get("diff")
	}
	if view == "source" && (e.Kind == "modify" || strings.HasPrefix(e.Kind, "script")) {
		first, _, _ := strings.Cut(content, "\n")
		if strings.HasPrefix(first, "#!") {
			for _, candidate := range []struct{ needle, name string }{{"pwsh", "powershell"}, {"powershell", "powershell"}, {"python", "python"}, {"ruby", "ruby"}, {"perl", "perl"}, {"fish", "fish"}, {"bash", "bash"}, {"zsh", "bash"}, {"/sh", "bash"}, {" sh", "bash"}} {
				if strings.Contains(first, candidate.needle) {
					return lexers.Get(candidate.name)
				}
			}
		}
		if lexer := lexers.Match(strings.TrimSuffix(filepath.Base(e.Source), ".tmpl")); lexer != nil {
			return lexer
		}
		if lexer := lexers.Analyse(content); lexer != nil {
			return lexer
		}
	}
	name := strings.TrimSuffix(filepath.Base(e.Target), ".tmpl")
	if lexer := lexers.Match(name); lexer != nil {
		return lexer
	}
	if lexer := lexers.Analyse(content); lexer != nil {
		return lexer
	}
	return lexers.Fallback
}

var templateExpression = regexp.MustCompile(`(?s)\{\{.*?\}\}`)

func highlight(content string, e chezmoi.Entry, view string, color bool) string {
	content = strings.ReplaceAll(sanitize(content), "\t", "    ")
	if !color || len(content) > 1<<20 || e.Encrypted && view == "source" {
		return content
	}
	lexer := lexerFor(e, view, content)
	if lexer == nil {
		return content
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, content)
	if err != nil {
		return content
	}
	if e.Template && view == "source" {
		iterator = templateTokens(iterator, content)
	}
	var out bytes.Buffer
	style := styles.Get("dracula")
	if style == nil {
		style = styles.Fallback
	}
	if err := formatters.Get("terminal256").Format(&out, style, iterator); err != nil {
		return content
	}
	return out.String()
}

// Overlay template ranges while retaining the underlying TOML/YAML/etc. lexer.
// Token boundaries may bisect a template range, so offsets are in source bytes.
func templateTokens(iterator chroma.Iterator, content string) chroma.Iterator {
	ranges := templateExpression.FindAllStringIndex(content, -1)
	if len(ranges) == 0 {
		return iterator
	}
	var tokens []chroma.Token
	offset, index := 0, 0
	for token := iterator(); token != chroma.EOF; token = iterator() {
		value := token.Value
		for len(value) > 0 {
			for index < len(ranges) && ranges[index][1] <= offset {
				index++
			}
			length := len(value)
			kind := token.Type
			if index < len(ranges) {
				r := ranges[index]
				if r[0] <= offset {
					kind = chroma.CommentPreproc
					length = min(length, r[1]-offset)
				} else {
					length = min(length, r[0]-offset)
				}
			}
			tokens = append(tokens, chroma.Token{Type: kind, Value: value[:length]})
			value = value[length:]
			offset += length
		}
	}
	return chroma.Literator(tokens...)
}
