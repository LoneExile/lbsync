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

func syncOne(cfg config, sub *Subsonic, gen Generated) (bool, error) {
	name := fmt.Sprintf("%s for %s", gen.Label(), cfg.lbUser)
	logf("  %s", gen.Label())
	logf("    source : %s  (%s)", gen.Title, firstN(gen.Date, 19))

	lbTracks, err := Tracks(gen.MBID)
	if err != nil {
		return false, err
	}
	logf("    tracks : %d on ListenBrainz", len(lbTracks))
	if len(lbTracks) == 0 {
		logf("    nothing to do (empty playlist)")
		return true, nil
	}

	var ids []string
	seen := map[string]bool{}
	var rejected []rejection
	cache := map[string][]Candidate{}
	for _, want := range lbTracks {
		key := Normalize(want.Title)
		cands, ok := cache[key]
		if !ok {
			cands, err = sub.Search3(want.Title)
			if err != nil {
				logf("    search failed for %q: %v", want.Title, err)
				cands = nil
			}
			cache[key] = cands
		}
		if id, found := Resolve(want, cands); found {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
			continue
		}
		rejected = append(rejected, rejection{
			Artist: want.Artist, Title: want.Title,
			Album: want.Album, RecordingMBID: want.RecordingMBID,
		})
	}
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
		b, _ := json.MarshalIndent(wl, "", "  ")
		if err := os.WriteFile(cfg.rejectionsPath, append(b, '\n'), 0o644); err != nil {
			logf("    WARNING: could not write rejections worklist: %v", err)
		} else {
			logf("    worklist: %d unmatched tracks -> %s", len(rejected), cfg.rejectionsPath)
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
		// Newest first: available is already sorted by date descending.
		var newest Generated
		for _, g := range available {
			if g.Type == t {
				newest = g
				break
			}
		}
		ok, err := syncOne(cfg, sub, newest)
		if err != nil {
			logf("    FAILED: %v", err)
			ok = false
		}
		if !ok {
			failures++
		}
		logf("")
	}

	if failures > 0 {
		logf("lbsync: %d playlist(s) failed", failures)
		return 1
	}
	logf("lbsync: done")
	return 0
}

func main() { os.Exit(run()) }
