// Command lbsync syncs ListenBrainz generated playlists into a
// Subsonic/Navidrome server.
//
// ListenBrainz builds playlists for you (Weekly Jams, Weekly Exploration,
// Daily Jams, ...) but they only exist on the website. Subsonic clients can
// only see what is in your own server, so this writes them in as real
// playlists, updated in place on every run.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type config struct {
	subsonicURL      string
	subsonicUser     string
	subsonicPassword string
	lbUser           string
	playlists        []string
	includeLastWeek  bool
	public           bool
	dryRun           bool
	minMatched       int
	rejectionsPath   string
}

func env(name, def string) string {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		return v
	}
	return def
}

func envBool(name string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(env(name, "")))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func loadConfig() (config, error) {
	c := config{
		subsonicURL:      env("SUBSONIC_URL", ""),
		subsonicUser:     env("SUBSONIC_USER", ""),
		subsonicPassword: env("SUBSONIC_PASSWORD", ""),
		lbUser:           env("LISTENBRAINZ_USER", ""),
		includeLastWeek:  envBool("INCLUDE_LAST_WEEK", true),
		public:           envBool("PLAYLIST_PUBLIC", true),
		dryRun:           envBool("DRY_RUN", false),
		rejectionsPath:   env("REJECTIONS_PATH", ""),
	}
	for _, s := range strings.Split(env("PLAYLISTS", ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.playlists = append(c.playlists, s)
		}
	}
	n, err := strconv.Atoi(env("MIN_MATCHED", "1"))
	if err != nil || n < 0 {
		return c, fmt.Errorf("MIN_MATCHED must be a non-negative integer, got %q", env("MIN_MATCHED", ""))
	}
	c.minMatched = n

	var missing []string
	for name, val := range map[string]string{
		"SUBSONIC_URL": c.subsonicURL, "SUBSONIC_USER": c.subsonicUser,
		"SUBSONIC_PASSWORD": c.subsonicPassword, "LISTENBRAINZ_USER": c.lbUser,
	} {
		if val == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return c, fmt.Errorf("missing required environment: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// rejection is a ListenBrainz track that is not in the library. Collected into
// a worklist: the strict matcher's rejections ARE the acquisition list, so they
// are a first-class output rather than a discarded log line.
type rejection struct {
	Artist        string `json:"artist"`
	Title         string `json:"title"`
	Album         string `json:"album,omitempty"`
	RecordingMBID string `json:"recording_mbid,omitempty"`
}

type worklist struct {
	GeneratedAt      string      `json:"generated_at"`
	ListenBrainzUser string      `json:"listenbrainz_user"`
	Playlist         string      `json:"playlist"`
	Source           string      `json:"source"`
	Requested        int         `json:"requested"`
	Matched          int         `json:"matched"`
	NotInLibrary     []rejection `json:"not_in_library"`
}

func logf(format string, a ...any) { fmt.Printf(format+"\n", a...) }

// resolveAll maps ListenBrainz tracks onto library song IDs, returning the
// matched IDs (de-duplicated, in order) and the tracks that are not owned.
//
// Split out of syncOne so the resolution rules can be exercised without a
// network — the matcher tests cover Normalize/Matches, but only this can cover
// how a whole track list is assembled.
func resolveAll(sub *Subsonic, want []Track) ([]string, []rejection) {
	var ids []string
	seen := map[string]bool{}
	var rejected []rejection
	cache := map[string][]Candidate{}
	for _, w := range want {
		key := Normalize(w.Title)
		cands, ok := cache[key]
		if !ok {
			var err error
			cands, err = sub.Search3(w.Title)
			if err != nil {
				logf("    search failed for %q: %v", w.Title, err)
				cands = nil
			}
			cache[key] = cands
		}
		if id, found := Resolve(w, cands); found {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
			continue
		}
		rejected = append(rejected, rejection{
			Artist: w.Artist, Title: w.Title,
			Album: w.Album, RecordingMBID: w.RecordingMBID,
		})
	}
	return ids, rejected
}

func syncOne(cfg config, sub *Subsonic, a Assignment) (bool, error) {
	logf("  %s", a.Source.Label())
	logf("    source : %s  (%s)", a.Source.Title, firstN(a.Source.Date, 19))
	lbTracks, err := Tracks(a.Source.MBID)
	if err != nil {
		return false, err
	}
	return syncTracks(cfg, sub, a.Name, a.Source, lbTracks)
}

// syncTracks writes one playlist and verifies the result.
//
// It takes the track list explicitly rather than fetching it, so the WRITE path
// can be driven by a fake server. That matters: the two nastiest bugs here were
// both in the write path (createPlaylist requiring `name`, and silently ignoring
// `public`) and neither is visible to a matcher test.
func syncTracks(cfg config, sub *Subsonic, name string, gen Generated, lbTracks []Track) (bool, error) {
	logf("    tracks : %d on ListenBrainz", len(lbTracks))
	if len(lbTracks) == 0 {
		logf("    nothing to do (empty playlist)")
		return true, nil
	}
	ids, rejected := resolveAll(sub, lbTracks)
	logf("    owned  : %d of %d (not in library: %d)", len(ids), len(lbTracks), len(rejected))

	if cfg.rejectionsPath != "" && len(rejected) > 0 {
		wl := worklist{
			GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
			ListenBrainzUser: cfg.lbUser,
			Playlist:         name,
			Source:           gen.Title,
			Requested:        len(lbTracks),
			Matched:          len(ids),
			NotInLibrary:     rejected,
		}
		// REJECTIONS_PATH is a DIRECTORY: one file per playlist. A single shared
		// path meant each playlist in a run overwrote the previous one's list,
		// so all but the last were silently lost.
		b, _ := json.MarshalIndent(wl, "", "  ")
		dest := filepath.Join(cfg.rejectionsPath, slug(name)+".json")
		if err := os.MkdirAll(cfg.rejectionsPath, 0o755); err != nil {
			logf("    WARNING: could not create worklist dir: %v", err)
		} else if err := os.WriteFile(dest, append(b, '\n'), 0o644); err != nil {
			logf("    WARNING: could not write rejections worklist: %v", err)
		} else {
			logf("    worklist: %d unmatched tracks -> %s", len(rejected), dest)
		}
	}

	// Never overwrite a playlist with nothing: an empty match set means
	// something upstream broke, not that the playlist should be emptied.
	if len(ids) < cfg.minMatched {
		logf("    SKIP: only %d matched (< MIN_MATCHED=%d); leaving the existing playlist untouched",
			len(ids), cfg.minMatched)
		return false, nil
	}

	existing, found, err := sub.PlaylistByName(name)
	if err != nil {
		return false, err
	}
	if found {
		logf("    target : %q (id %s, %d tracks)", name, existing.ID, existing.SongCount)
	} else {
		logf("    target : %q (new)", name)
	}
	if cfg.dryRun {
		logf("    DRY_RUN: not writing")
		return true, nil
	}

	playlistID := ""
	if found {
		playlistID = existing.ID
	}
	if err := sub.CreatePlaylist(playlistID, name, ids); err != nil {
		return false, err
	}

	// Verify the OBSERVED state, never the requested one.
	after, found, err := sub.PlaylistByName(name)
	if err != nil {
		return false, err
	}
	if !found {
		logf("    FAILED: playlist %q not present after writing", name)
		return false, nil
	}
	if cfg.public && !after.Public {
		if err := sub.SetPublic(after.ID, true); err != nil {
			logf("    WARNING: could not make playlist public: %v", err)
		}
		after, _, err = sub.PlaylistByName(name)
		if err != nil {
			return false, err
		}
	}
	if after.SongCount != len(ids) {
		logf("    FAILED: wrote %d tracks but server reports %d", len(ids), after.SongCount)
		return false, nil
	}
	if cfg.public && !after.Public {
		logf("    FAILED: playlist is NOT public, so it will not be visible in clients other than its owner")
		return false, nil
	}
	vis := "private"
	if after.Public {
		vis = "public"
	}
	logf("    OK: %q now has %d tracks (%s)", name, after.SongCount, vis)
	return true, nil
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func run() int {
	cfg, err := loadConfig()
	if err != nil {
		logf("lbsync: %v", err)
		return 1
	}
	sub := NewSubsonic(cfg.subsonicURL, cfg.subsonicUser, cfg.subsonicPassword)
	if err := sub.Ping(); err != nil {
		logf("lbsync: cannot reach Subsonic: %v", err)
		return 1
	}

	available, err := CreatedFor(cfg.lbUser)
	if err != nil {
		logf("lbsync: %v", err)
		return 1
	}
	if len(available) == 0 {
		logf("lbsync: ListenBrainz offers no generated playlists for %q", cfg.lbUser)
		return 0
	}

	typeSet := map[string]bool{}
	var types []string
	for _, g := range available {
		if !typeSet[g.Type] {
			typeSet[g.Type] = true
			types = append(types, g.Type)
		}
	}
	sort.Strings(types)
	logf("ListenBrainz offers for %q: %s", cfg.lbUser, strings.Join(types, ", "))

	wanted := map[string]bool{}
	for _, t := range cfg.playlists {
		wanted[t] = true
	}
	var selected []string
	for _, t := range types {
		if len(wanted) == 0 || wanted[t] {
			selected = append(selected, t)
		}
	}
	if len(selected) == 0 {
		logf("nothing selected (PLAYLISTS=%s)", strings.Join(cfg.playlists, ","))
		return 0
	}

	logf("")
	failures := 0
	for _, t := range selected {
		// available is already sorted newest-first.
		var gens []Generated
		for _, g := range available {
			if g.Type == t {
				gens = append(gens, g)
			}
		}
		// Newest fills the stable current-week name (replacing last week's
		// content in place); second-newest fills the stable "Last Week's" name.
		for _, a := range Assign(cfg.lbUser, gens, cfg.includeLastWeek) {
			ok, err := syncOne(cfg, sub, a)
			if err != nil {
				logf("    FAILED: %v", err)
				ok = false
			}
			if !ok {
				failures++
			}
			logf("")
		}
	}

	if failures > 0 {
		logf("lbsync: %d playlist(s) failed", failures)
		return 1
	}
	logf("lbsync: done")
	return 0
}

func main() { os.Exit(run()) }
