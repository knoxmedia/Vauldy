package docparse

import (
	"os"
	"regexp"
	"strings"
)

var reHTMLTags = regexp.MustCompile(`(?s)<[^>]+>`)

func readTextPreview(path string, maxLen int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxLen {
		data = data[:maxLen]
	}
	return strings.TrimSpace(string(data))
}

func stripHTMLPreview(s string) string {
	s = reHTMLTags.ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
