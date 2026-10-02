package service

import (
	"github.com/gaucho-racing/blix/knowledge/repository"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWikiSectionsPreserveFencesAndUnicode(t *testing.T) {
	content := "# Vehicle\nIntro\n## Brakes\n```python\n# Not a heading\n```\n" + strings.Repeat("车辆 pressure\n", 600) + "## Testing\nVerified.\n"
	title, season, sections := parseWikiPage(repository.Page{Path: "seasons/gr26/brakes.md", Content: content})
	if title != "Vehicle" || season != "gr26" {
		t.Fatal("metadata lost")
	}
	assembled := ""
	for _, section := range sections {
		if !utf8.ValidString(section.Content) || len(section.Content) > 2400 {
			t.Fatal("invalid section")
		}
		if strings.Contains(section.Heading, "Not a heading") {
			t.Fatal("code fence parsed as heading")
		}
		if content[section.Start:section.End] != section.Content {
			t.Fatal("section offsets incorrect")
		}
		assembled += section.Content
	}
	if assembled != content {
		t.Fatal("sectioning lost content")
	}
}
