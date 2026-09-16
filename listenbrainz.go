package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

const listenBrainzAPI = "https://api.listenbrainz.org/1"

var lbClient = &http.Client{Timeout: 30 * time.Second}

// Generated is one playlist ListenBrainz has generated for a user.
type Generated struct {
	Type       string // source_patch, e.g. "weekly-jams"
	Title      string // human title, which carries "week of YYYY-MM-DD Mon"
	Date       string // when ListenBrainz generated it
	MBID       string
	TrackCount int
}

// Label is the human name for a source_patch. Unknown types are title-cased, so
// a new ListenBrainz algorithm works without a code change.
func (g Generated) Label() string {
	if l, ok := map[string]string{
		"weekly-jams":        "Weekly Jams",
		"weekly-exploration": "Weekly Exploration",
		"daily-jams":         "Daily Jams",
	}[g.Type]; ok {
		return l
	}
	out := []rune(g.Type)
	for i, r := range out {
		if r == '-' {
			out[i] = ' '
			continue
		}
		if i == 0 || out[i-1] == ' ' {
			if r >= 'a' && r <= 'z' {
				out[i] = r - 32
			}
		}
	}
	return string(out)
}

func lbGet(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, listenBrainzAPI+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "lbsync")
	resp, err := lbClient.Do(req)
	if err != nil {
		return fmt.Errorf("listenbrainz %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("listenbrainz %s: reading body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("listenbrainz %s: HTTP %d: %.200s", path, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("listenbrainz %s: %w", path, err)
	}
	return nil
}

// CreatedFor returns every generated playlist ListenBrainz is currently
// offering this user, newest first.
//
// The list is only what ListenBrainz still serves: entries carry an expires_at
// roughly two weeks out, and older weeks drop off the API entirely.
func CreatedFor(user string) ([]Generated, error) {
	var raw struct {
		Playlists []struct {
			Playlist struct {
				Title      string `json:"title"`
				Date       string `json:"date"`
				Identifier string `json:"identifier"`
				Extension  map[string]struct {
					AdditionalMetadata struct {
						AlgorithmMetadata struct {
							SourcePatch string `json:"source_patch"`
						} `json:"algorithm_metadata"`
					} `json:"additional_metadata"`
				} `json:"extension"`
			} `json:"playlist"`
		} `json:"playlists"`
	}
	if err := lbGet("/user/"+url.PathEscape(user)+"/playlists/createdfor", &raw); err != nil {
		return nil, err
	}

	var out []Generated
	for _, e := range raw.Playlists {
		pl := e.Playlist
		meta, ok := pl.Extension["https://musicbrainz.org/doc/jspf#playlist"]
		if !ok || meta.AdditionalMetadata.AlgorithmMetadata.SourcePatch == "" {
			continue // not a generated playlist
		}
		parts := splitPath(pl.Identifier)
		if len(parts) == 0 {
			continue
		}
		out = append(out, Generated{
			Type:  meta.AdditionalMetadata.AlgorithmMetadata.SourcePatch,
			Title: pl.Title,
			Date:  pl.Date,
			MBID:  parts[len(parts)-1],
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out, nil
}

func splitPath(s string) []string {
	var parts []string
	cur := ""
	for _, r := range s {
		if r == '/' {
			if cur != "" {
				parts = append(parts, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return parts
}

// Tracks fetches the tracks of a playlist (JSPF).
func Tracks(mbid string) ([]Track, error) {
	var raw struct {
		Playlist struct {
			Track []struct {
				Creator    string   `json:"creator"`
				Title      string   `json:"title"`
				Album      string   `json:"album"`
				Identifier []string `json:"identifier"`
			} `json:"track"`
		} `json:"playlist"`
	}
	if err := lbGet("/playlist/"+url.PathEscape(mbid), &raw); err != nil {
		return nil, err
	}
	out := make([]Track, 0, len(raw.Playlist.Track))
	for _, t := range raw.Playlist.Track {
		if t.Creator == "" || t.Title == "" {
			continue
		}
		track := Track{Artist: t.Creator, Title: t.Title, Album: t.Album}
		for _, id := range t.Identifier {
			if parts := splitPath(id); len(parts) > 0 && len(parts) >= 2 && parts[len(parts)-2] == "recording" {
				track.RecordingMBID = parts[len(parts)-1]
			}
		}
		out = append(out, track)
	}
	return out, nil
}
