package service

import (
	"strings"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/repository"
)

func parseWikiPage(page repository.Page) (string, string, []model.WikiSection) {
	title := strings.TrimSuffix(page.Path, ".md")
	season := ""
	parts := strings.Split(page.Path, "/")
	if len(parts) > 2 && parts[0] == "seasons" {
		season = parts[1]
	}
	type boundary struct {
		offset  int
		heading string
	}
	boundaries := []boundary{{offset: 0, heading: title}}
	headings := []string{}
	offset := 0
	fence := ""
	for _, line := range strings.SplitAfter(page.Content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if marker == fence {
				fence = ""
			}
		} else if fence == "" {
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			if level > 0 && level <= 6 && len(trimmed) > level && trimmed[level] == ' ' {
				heading := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(trimmed[level:]), "#"))
				if heading != "" {
					if level == 1 && title == strings.TrimSuffix(page.Path, ".md") {
						title = heading
					}
					if level <= len(headings) {
						headings = headings[:level-1]
					}
					headings = append(headings, heading)
					label := strings.Join(headings, " / ")
					if offset == 0 {
						boundaries[0].heading = label
					} else {
						boundaries = append(boundaries, boundary{offset: offset, heading: label})
					}
				}
			}
		}
		offset += len(line)
	}
	sections := []model.WikiSection{}
	for index, b := range boundaries {
		end := len(page.Content)
		if index+1 < len(boundaries) {
			end = boundaries[index+1].offset
		}
		start := b.offset
		for start < end {
			chunkEnd := min(end, start+2400)
			if chunkEnd < end {
				if newline := strings.LastIndexByte(page.Content[start:chunkEnd], '\n'); newline > 1200 {
					chunkEnd = start + newline + 1
				}
				for !utf8.RuneStart(page.Content[chunkEnd]) {
					chunkEnd--
				}
			}
			content := page.Content[start:chunkEnd]
			if strings.TrimSpace(content) != "" {
				sections = append(sections, model.WikiSection{Path: page.Path, Position: len(sections), Heading: b.heading, Content: content, Start: start, End: chunkEnd, Season: season})
			}
			start = chunkEnd
		}
	}
	return title, season, sections
}
