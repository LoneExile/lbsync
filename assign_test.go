package main

import "testing"

func mk(t, d string) Generated {
	return Generated{Type: t, Title: t + " for x", Date: d}
}

func names(as []Assignment) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

func TestAssignRoles(t *testing.T) {
	gens := []Generated{
		mk("weekly-jams", "2026-09-14T00:08:16Z"),
		mk("weekly-jams", "2026-08-24T00:13:54Z"),
	}
	got := names(Assign("loneexile", gens, true))
	want := []string{"Weekly Jams for loneexile", "Last Week's Jams for loneexile"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Assign = %v, want %v", got, want)
	}
}

// The current-week name must NOT change when the week rolls over: that is what
// makes each run replace the previous week in place instead of orphaning it.
func TestCurrentNameIsStableAcrossWeeks(t *testing.T) {
	a := Assign("u", []Generated{mk("weekly-jams", "2026-09-14T00:00:00Z")}, true)
	b := Assign("u", []Generated{mk("weekly-jams", "2026-09-21T00:00:00Z")}, true)
	if a[0].Name != b[0].Name {
		t.Fatalf("current name changed across weeks: %q vs %q", a[0].Name, b[0].Name)
	}
}

// With only one entry live (ListenBrainz has expired the older one) the last-week
// playlist must simply not be written, rather than being emptied — emptying it
// would destroy the only copy of those tracks.
func TestSingleEntryDoesNotWriteLastWeek(t *testing.T) {
	got := names(Assign("u", []Generated{mk("weekly-exploration", "2026-09-14T00:00:00Z")}, true))
	if len(got) != 1 || got[0] != "Weekly Exploration for u" {
		t.Fatalf("Assign = %v, want just the current playlist", got)
	}
}

func TestIncludeLastWeekCanBeDisabled(t *testing.T) {
	gens := []Generated{mk("weekly-jams", "2026-09-14T00:00:00Z"), mk("weekly-jams", "2026-08-24T00:00:00Z")}
	if got := names(Assign("u", gens, false)); len(got) != 1 {
		t.Fatalf("Assign = %v, want 1 (last-week disabled)", got)
	}
}

func TestAssignEmpty(t *testing.T) {
	if got := Assign("u", nil, true); len(got) != 0 {
		t.Fatalf("Assign(nil) = %v, want empty", got)
	}
}

func TestLabelFallbackForUnknownType(t *testing.T) {
	g := Generated{Type: "monthly-discovery"}
	if got := g.Label(); got != "Monthly Discovery" {
		t.Fatalf("Label() = %q, want %q", got, "Monthly Discovery")
	}
}

// Each playlist needs its own worklist file; a shared path silently dropped all
// but the last playlist's rejections.
func TestSlugDistinguishesPlaylists(t *testing.T) {
	a := slug("Weekly Jams for loneexile")
	b := slug("Last Week's Jams for loneexile")
	if a == b {
		t.Fatalf("slug collision: %q == %q", a, b)
	}
	for _, s := range []string{a, b} {
		for _, r := range s {
			if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				t.Fatalf("slug %q contains an unsafe character %q", s, r)
			}
		}
	}
	if slug("") != "" {
		t.Fatalf("slug(\"\") = %q, want empty", slug(""))
	}
}
