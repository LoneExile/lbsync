package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Subsonic is a minimal client for the endpoints this tool needs. It uses token
// auth (salt + md5) so the password itself never travels on the wire.
type Subsonic struct {
	baseURL string
	user    string
	token   string
	salt    string
	http    *http.Client
}

// Playlist is a playlist as reported by getPlaylists.
type Playlist struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SongCount int    `json:"songCount"`
	Public    bool   `json:"public"`
	Changed   string `json:"changed"`
}

func NewSubsonic(baseURL, user, password string) *Subsonic {
	salt := fmt.Sprintf("%d", time.Now().UnixNano())
	// A Subsonic "enc:" value is already hex-encoded and is sent as the token.
	token := strings.TrimPrefix(password, "enc:")
	if !strings.HasPrefix(password, "enc:") {
		sum := md5.Sum([]byte(password + salt))
		token = hex.EncodeToString(sum[:])
	}
	return &Subsonic{
		baseURL: strings.TrimRight(baseURL, "/"),
		user:    user,
		token:   token,
		salt:    salt,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type subsonicEnvelope struct {
	Response struct {
		Status string `json:"status"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		SearchResult3 struct {
			Songs []struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				Artist string `json:"artist"`
			} `json:"song"`
		} `json:"searchResult3"`
		Playlists struct {
			Playlists []Playlist `json:"playlist"`
		} `json:"playlists"`
	} `json:"subsonic-response"`
}

func (s *Subsonic) call(method string, params url.Values) (*subsonicEnvelope, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("u", s.user)
	params.Set("t", s.token)
	params.Set("s", s.salt)
	params.Set("v", "1.16.1")
	params.Set("c", "lbsync")
	params.Set("f", "json")

	endpoint := fmt.Sprintf("%s/rest/%s.view?%s", s.baseURL, method, params.Encode())
	resp, err := s.http.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("subsonic %s: %w", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("subsonic %s: reading body: %w", method, err)
	}
	var env subsonicEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("subsonic %s: not JSON (HTTP %d): %.200s", method, resp.StatusCode, body)
	}
	if env.Response.Status != "ok" {
		if e := env.Response.Error; e != nil {
			return nil, fmt.Errorf("subsonic %s: %d %s", method, e.Code, e.Message)
		}
		return nil, fmt.Errorf("subsonic %s: status %q", method, env.Response.Status)
	}
	return &env, nil
}

func (s *Subsonic) Ping() error {
	_, err := s.call("ping", nil)
	return err
}

// Search3 looks up candidates by TITLE ONLY.
//
// Two deliberate choices, both measured against a live server:
//
//   - The query is the title alone. A combined "artist title" phrase is a long
//     multi-term string that server-side search handles poorly.
//   - artistCount/albumCount are NOT sent. Passing 0 for either makes Navidrome
//     return an empty searchResult3 for EVERY query, which looks exactly like an
//     empty library.
//
// Precision comes from Matches, not from the query.
func (s *Subsonic) Search3(title string) ([]Candidate, error) {
	params := url.Values{}
	params.Set("query", title)
	params.Set("songCount", "100")
	env, err := s.call("search3", params)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(env.Response.SearchResult3.Songs))
	for _, s := range env.Response.SearchResult3.Songs {
		out = append(out, Candidate{ID: s.ID, Artist: s.Artist, Title: s.Title})
	}
	return out, nil
}

func (s *Subsonic) Playlists() ([]Playlist, error) {
	env, err := s.call("getPlaylists", nil)
	if err != nil {
		return nil, err
	}
	return env.Response.Playlists.Playlists, nil
}

func (s *Subsonic) PlaylistByName(name string) (Playlist, bool, error) {
	all, err := s.Playlists()
	if err != nil {
		return Playlist{}, false, err
	}
	for _, p := range all {
		if p.Name == name {
			return p, true, nil
		}
	}
	return Playlist{}, false, nil
}

// CreatePlaylist creates or (with playlistID) updates a playlist.
//
// `name` is always sent: Subsonic requires it when creating and merely tolerates
// it when updating, so sending it unconditionally keeps one code path.
func (s *Subsonic) CreatePlaylist(playlistID, name string, songIDs []string) error {
	params := url.Values{}
	params.Set("name", name)
	if playlistID != "" {
		params.Set("playlistId", playlistID)
	}
	for _, id := range songIDs {
		params.Add("songId", id)
	}
	_, err := s.call("createPlaylist", params)
	return err
}

// SetPublic is the call that actually makes a playlist visible to other users.
//
// Navidrome IGNORES public= on createPlaylist: a newly created playlist comes
// back public=false no matter what was requested. Because playlists are owned by
// the configured user (not by the client's user), a private playlist is
// invisible to everyone else — which defeats the entire point of the tool. Only
// updatePlaylist sets it.
func (s *Subsonic) SetPublic(playlistID string, public bool) error {
	params := url.Values{}
	params.Set("playlistId", playlistID)
	if public {
		params.Set("public", "true")
	} else {
		params.Set("public", "false")
	}
	_, err := s.call("updatePlaylist", params)
	return err
}
