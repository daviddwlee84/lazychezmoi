package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazychezmoi/internal/chezmoi"
)

func TestTemplateUsesUnderlyingLanguageAndKeepsContent(t *testing.T) {
	e := chezmoi.Entry{Target: "/home/.config/tool/config.toml", Source: "/source/dot_config/tool/config.toml.tmpl", Template: true, Kind: "file"}
	source := "[settings]\nname = {{ .name | quote }}\ncount = 12\n"
	lexer := lexerFor(e, "source", source)
	if !strings.EqualFold(lexer.Config().Name, "TOML") {
		t.Fatalf("wrong lexer: %s", lexer.Config().Name)
	}
	got := highlight(source, e, "source", true)
	if ansi.Strip(got) != source {
		t.Fatalf("highlight altered source:\n%q", ansi.Strip(got))
	}
	if !strings.Contains(got, "\x1b[") {
		t.Fatal("expected syntax highlighting")
	}
}

func TestModifyUsesShebangAndRenderedTargetLanguage(t *testing.T) {
	e := chezmoi.Entry{Target: "/home/config.toml", Source: "/source/modify_config.toml", Kind: "modify"}
	source := "#!/usr/bin/env python3\nprint('hello')\n"
	if name := lexerFor(e, "source", source).Config().Name; !strings.EqualFold(name, "Python") {
		t.Fatalf("modify source used %s", name)
	}
	if name := lexerFor(e, "rendered", "a=1\n").Config().Name; !strings.EqualFold(name, "TOML") {
		t.Fatalf("rendered used %s", name)
	}
}

func TestEncryptedSourceStaysCiphertext(t *testing.T) {
	e := chezmoi.Entry{Target: "/home/config.toml", Source: "/source/encrypted_config.toml.tmpl.age", Kind: "file", Template: true, Encrypted: true}
	ciphertext := "-----BEGIN AGE ENCRYPTED FILE-----\nfixture\n-----END AGE ENCRYPTED FILE-----\n"
	if got := highlight(ciphertext, e, "source", true); got != ciphertext {
		t.Fatal("encrypted source must remain plain ciphertext")
	}
	if got := highlight("a = 1\n", e, "rendered", true); !strings.Contains(got, "\x1b[") {
		t.Fatal("rendered plaintext should still receive syntax highlighting")
	}
}
