package ui

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// readmeKeys returns the key cells of README.md's "Keys" table.
func readmeKeys(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cells []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "## Keys":
			in = true
		case in && strings.HasPrefix(line, "|"):
			cell := strings.TrimSpace(strings.Split(line, "|")[1])
			if cell != "Key" && !strings.HasPrefix(cell, "--") {
				cells = append(cells, cell)
			}
		case in && len(cells) > 0:
			return cells
		}
	}
	if len(cells) == 0 {
		t.Fatal("no Keys table in README.md")
	}
	return cells
}

// keyTokens splits key cells into single key names ("PageDown", "+", "?").
func keyTokens(cells []string) []string {
	var toks []string
	for _, c := range cells {
		for _, k := range strings.Fields(strings.ReplaceAll(c, ",", " ")) {
			if k != "/" && !slices.Contains(toks, k) {
				toks = append(toks, k)
			}
		}
	}
	return toks
}

// TestHelpMatchesReadme: the help names exactly the keys of README's table.
func TestHelpMatchesReadme(t *testing.T) {
	var cells []string
	for _, g := range helpGroups {
		for _, r := range g {
			cells = append(cells, r.keys)
		}
	}
	readme, help := keyTokens(readmeKeys(t)), keyTokens(cells)
	for _, k := range readme {
		if !slices.Contains(help, k) {
			t.Errorf("README key %q missing from the help", k)
		}
	}
	for _, k := range help {
		if !slices.Contains(readme, k) {
			t.Errorf("help key %q not in README's table", k)
		}
	}
	// The keys also come through the layout (wrapping must not lose any).
	var shown []string
	for _, l := range strings.Split(helpText(), "\n") {
		k := []rune(l)[:min(helpKeyWidth, utf8.RuneCountInString(l))]
		shown = append(shown, string(k))
	}
	for _, k := range readme {
		if !slices.Contains(keyTokens(shown), k) {
			t.Errorf("key %q not in the key column of helpText", k)
		}
	}
}

func TestHelpTextLayout(t *testing.T) {
	text := helpText()
	lines := strings.Split(text, "\n")
	if last := lines[len(lines)-1]; last == "" {
		t.Error("ends with a blank line")
	}
	blank := 0
	for i, l := range lines {
		r := []rune(l)
		if len(r) > helpWidth {
			t.Errorf("line %d is %d wide: %q", i, len(r), l)
		}
		if l == "" {
			blank++
			continue
		}
		if l != strings.TrimRight(l, " ") {
			t.Errorf("line %d has trailing spaces: %q", i, l)
		}
		// Actions start in one column: the gap before it is blank, and a
		// key never runs into it.
		col := helpKeyWidth + helpGap
		if len(r) > helpKeyWidth && strings.TrimSpace(string(r[helpKeyWidth:col])) != "" {
			t.Errorf("line %d: key column overflows: %q", i, l)
		}
		if len(r) > col && r[col] == ' ' {
			t.Errorf("line %d: action not at column %d: %q", i, col, l)
		}
	}
	// One blank line between groups.
	if want := len(helpGroups) - 1; blank != want {
		t.Errorf("%d blank lines, want %d", blank, want)
	}
	if !strings.Contains(text, "PageDown, Space,    next image\nRight, Down, n\n") {
		t.Errorf("wrapped key list not as expected:\n%s", text)
	}
}

func TestWrapWords(t *testing.T) {
	for _, c := range []struct {
		s     string
		width int
		want  []string
	}{
		{"", 10, nil},
		{"one", 10, []string{"one"}},
		{"one two three", 7, []string{"one two", "three"}},
		{"rotate 90° left", 10, []string{"rotate 90°", "left"}},
		{"averyveryverylongword x", 5, []string{"averyveryverylongword", "x"}},
	} {
		if got := wrapWords(c.s, c.width); !slices.Equal(got, c.want) {
			t.Errorf("wrapWords(%q, %d) = %q, want %q", c.s, c.width, got, c.want)
		}
	}
}
