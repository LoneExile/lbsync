package main

import (
	"regexp"
	"strings"
)

// Track is a ListenBrainz recommendation: what we want to find in the library.
type Track struct {
	Artist string `json:"artist"`
	Title  string `json:"title"`
	// RecordingMBID and Album are carried through to the rejection worklist so
	// something downstream can act on them. They are never used for matching:
	// Subsonic does not expose recording MBIDs, so ID matching cannot work
	// against a real library.
	RecordingMBID string `json:"recording_mbid,omitempty"`
	Album         string `json:"album,omitempty"`
}

// Candidate is a track returned by the Subsonic search.
type Candidate struct {
	ID     string
	Artist string
	Title  string
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

// Normalize canonicalises a string for comparison: case-folded, with
// punctuation and spacing removed, so "Worth It." matches "Worth It" and
// "Ne‐Yo" (non-breaking hyphen, which ListenBrainz emits) matches "Ne-Yo".
//
// CRITICAL: it must never return the empty string for non-empty input. Every
// CJK title strips to "" because it contains no a-z0-9, and "" == "" would make
// any two unrelated CJK tracks compare equal — binding whichever the server
// happened to return first. Falling back to the raw lowercased string keeps CJK
// an exact match, so only genuinely identical titles compare equal.
func Normalize(s string) string {
	s = strings.ToLower(s)
	stripped := nonAlnum.ReplaceAllString(s, "")
	if stripped == "" {
		return s
	}
	return stripped
}

// Matches reports whether a library candidate is the same track as want.
//
// Both the title AND the artist must match. There is deliberately no fallback
// to a same-title/other-artist candidate: measured against a real 50-track
// ListenBrainz list, strict matching resolved 6 tracks where a fallback
// "resolved" 16 — and the 10 extras were wrong songs (it substituted Kings of
// Leon's "Notion" for The Rare Occasions' "Notion", which the library did not
// contain). A short playlist of real tracks beats a long one full of impostors.
func Matches(want Track, c Candidate) bool {
	return Normalize(c.Title) == Normalize(want.Title) &&
		Normalize(c.Artist) == Normalize(want.Artist)
}

// Resolve returns the first candidate that is genuinely the wanted track.
func Resolve(want Track, candidates []Candidate) (string, bool) {
	for _, c := range candidates {
		if Matches(want, c) {
			return c.ID, true
		}
	}
	return "", false
}
