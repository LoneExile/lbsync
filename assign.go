package main

import (
	"fmt"
	"strings"
)

// Assignment is one playlist to write: the name it carries in Subsonic, and the
// ListenBrainz entry it mirrors.
type Assignment struct {
	Name     string
	Source   Generated
	LastWeek bool
}

// PlaylistName builds the Subsonic name for a role.
//
// Names are role-based and stable, not dated. That is deliberate:
//
//   - the current-week name is the SAME every week, so each run overwrites the
//     previous week's content in place — the playlist is never orphaned and no
//     migration is needed when the mirroring scheme changes;
//   - the "Last Week's" name is likewise stable, so it holds exactly one week
//     and older weeks disappear on their own as it is overwritten.
//
// Dated names would instead accumulate a playlist per week and require pruning
// to stop the list growing forever.
// shortLabel drops the "Weekly " prefix for the last-week role, so the playlist
// reads "Last Week's Jams for X" — matching the card ListenBrainz actually
// shows — rather than the stuttering "Last Week's Weekly Jams for X".
//
// Only "Weekly " is stripped: a hypothetical "Daily Jams" keeps its prefix, so
// the two can never collide on the same playlist name.
func shortLabel(label string) string {
	return strings.TrimPrefix(label, "Weekly ")
}

func PlaylistName(label, lbUser string, lastWeek bool) string {
	if lastWeek {
		return fmt.Sprintf("Last Week's %s for %s", shortLabel(label), lbUser)
	}
	return fmt.Sprintf("%s for %s", label, lbUser)
}

// Assign maps the entries ListenBrainz offers for ONE playlist type onto the
// (at most two) playlists kept for it. gens must be newest-first.
//
// This mirrors the four cards ListenBrainz shows: this week's Jams, this week's
// Exploration, and the "Last Week's" pair. It is a pure function so the mapping
// is testable without a server.
func Assign(lbUser string, gens []Generated, includeLastWeek bool) []Assignment {
	if len(gens) == 0 {
		return nil
	}
	out := []Assignment{{
		Name:   PlaylistName(gens[0].Label(), lbUser, false),
		Source: gens[0],
	}}
	if includeLastWeek && len(gens) > 1 {
		out = append(out, Assignment{
			Name:     PlaylistName(gens[1].Label(), lbUser, true),
			Source:   gens[1],
			LastWeek: true,
		})
	}
	return out
}

// slug turns a playlist name into a filename-safe token, used to give each
// playlist its own worklist file. Without this, every type in a single run
// writes to the same path and all but the last are silently lost.
func slug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
