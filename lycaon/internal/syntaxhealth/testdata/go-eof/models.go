// Package models defines the data types shared by store, API, and staticgen.
package models

import "time"

type Post struct {
	ID          int64
	Slug        string
	Title       string
	Content     string
	Excerpt     string
	Tags        string
	Draft       bool
	PublishedAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TagList splits the comma-separated tags field.
func (p Post) TagList() []string {
	var out []string
	for _, t := range splitComma(p.Tags) {
		out = append(out, t)
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if c := trimSpace(cur); c != "" {
				out = append(out, c)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if c := trimSpace(cur); c != "" {
		out = append(out, c)
	}
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

type Image struct {
	ID        int64
	Filename  string
	Mime      string
	Size      int64
	CreatedAt time.Time
}

type SearchResult struct {
	ID      int64
	Slug    string
	Title   string
	Excerpt string
	Snippet string
}