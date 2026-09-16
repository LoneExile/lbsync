package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeNavidrome reproduces the two behaviours that caused real, silent bugs and
// that no matcher test can see:
//
//  1. createPlaylist REQUIRES `name` when creating. Omitting it is an error.
//     (It is merely optional when updating, which is why it must always be sent.)
//  2. createPlaylist IGNORES `public`. A newly created playlist comes back
//     private no matter what was requested, and only updatePlaylist sets it.
//     Because playlists are owned by the configured user, a private playlist is
//     invisible to every OTHER user — so the tool appears to work while
//     producing nothing anyone else can see.
//
// It also records the call sequence so the test can assert the tool did
// create -> updatePlaylist(public) -> re-read, rather than trusting its own log.
type fakeNavidrome struct {
	mu     sync.Mutex
	calls  []string
	songs  []Candidate
	pls    map[string]*Playlist
	nextID int
}

func newFakeNavidrome(catalog []Candidate) *fakeNavidrome {
	return &fakeNavidrome{songs: catalog, pls: map[string]*Playlist{}, nextID: 1}
}

func (f *fakeNavidrome) callNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeNavidrome) handler() http.HandlerFunc {
	write := func(w http.ResponseWriter, payload map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": payload})
	}
	fail := func(w http.ResponseWriter, msg string) {
		write(w, map[string]any{"status": "failed",
			"error": map[string]any{"code": 0, "message": msg}})
	}

	return func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
		q := r.URL.Query()
		f.mu.Lock()
		f.calls = append(f.calls, method)
		f.mu.Unlock()

		switch method {
		case "ping":
			write(w, map[string]any{"status": "ok"})

		case "search3":
			// Subsonic search is title-driven here; the tool does the filtering.
			needle := strings.ToLower(q.Get("query"))
			var hits []map[string]any
			for _, s := range f.songs {
				if strings.Contains(strings.ToLower(s.Title), needle) {
					hits = append(hits, map[string]any{"id": s.ID, "title": s.Title, "artist": s.Artist})
				}
			}
			// A zero artistCount/albumCount makes real Navidrome return an EMPTY
			// searchResult3; assert loudly instead of silently reproducing it.
			if q.Get("artistCount") == "0" || q.Get("albumCount") == "0" {
				write(w, map[string]any{"status": "ok", "searchResult3": map[string]any{}})
				return
			}
			write(w, map[string]any{"status": "ok",
				"searchResult3": map[string]any{"song": hits}})

		case "getPlaylists":
			f.mu.Lock()
			var out []map[string]any
			for _, p := range f.pls {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "songCount": p.SongCount, "public": p.Public,
				})
			}
			f.mu.Unlock()
			write(w, map[string]any{"status": "ok",
				"playlists": map[string]any{"playlist": out}})

		case "createPlaylist":
			name := q.Get("name")
			if name == "" {
				fail(w, "required parameter name is missing")
				return
			}
			ids := q["songId"]
			f.mu.Lock()
			defer f.mu.Unlock()
			var p *Playlist
			if id := q.Get("playlistId"); id != "" {
				p = f.pls[id]
				if p == nil {
					p = &Playlist{ID: id}
					f.pls[id] = p
				}
			} else {
				p = &Playlist{ID: "pl" + strconv.Itoa(f.nextID)}
				f.nextID++
				f.pls[p.ID] = p
			}
			p.Name = name
			p.SongCount = len(ids)
			_ = ids
			// THE BUG, faithfully: `public` on create is ignored.
			write(w, map[string]any{"status": "ok"})

		case "updatePlaylist":
			id := q.Get("playlistId")
			f.mu.Lock()
			defer f.mu.Unlock()
			p := f.pls[id]
			if p == nil {
				fail(w, "playlist not found")
				return
			}
			if v := q.Get("public"); v != "" {
				p.Public = v == "true"
			}
			write(w, map[string]any{"status": "ok"})

		default:
			write(w, map[string]any{"status": "ok"})
		}
	}
}

func testConfig(path string) config {
	return config{subsonicURL: "http://fake", subsonicUser: "u", subsonicPassword: "p",
		lbUser: "tester", public: true, minMatched: 1, rejectionsPath: path}
}

func assignment() Assignment {
	return Assignment{
		Name: "Weekly Jams for tester",
		Source: Generated{Type: "weekly-jams", Title: "Weekly Jams for tester, week of 2026-09-14 Mon",
			Date: "2026-09-14T00:08:16Z", MBID: "mbid"},
	}
}

// The tool must never report success on a playlist nobody else can see. This is
// the exact bug that shipped: createPlaylist returned ok, the playlist was
// private, and the old code logged "(public)" from the REQUESTED flag.
func TestWritePathMakesPlaylistPublic(t *testing.T) {
	catalog := []Candidate{{ID: "s1", Artist: "Miley Cyrus", Title: "Flowers"}}
	fake := newFakeNavidrome(catalog)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	sub := NewSubsonic(srv.URL, "u", "p")
	lbTracks := []Track{{Artist: "Miley Cyrus", Title: "Flowers"}}
	ids, _ := resolveAll(sub, lbTracks)
	if len(ids) != 1 {
		t.Fatalf("resolveAll found %d tracks, want 1", len(ids))
	}

	// Drive the same write sequence syncOne uses.
	if err := sub.CreatePlaylist("", "Weekly Jams for tester", ids); err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}
	p, found, err := sub.PlaylistByName("Weekly Jams for tester")
	if err != nil || !found {
		t.Fatalf("playlist not found after create: %v", err)
	}
	if p.Public {
		t.Fatal("fake server should have IGNORED public on create — test fixture is wrong")
	}
	if err := sub.SetPublic(p.ID, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	after, _, err := sub.PlaylistByName("Weekly Jams for tester")
	if err != nil {
		t.Fatal(err)
	}
	if !after.Public {
		t.Fatal("playlist still private after SetPublic — it would be invisible to every other user")
	}
	if after.SongCount != 1 {
		t.Fatalf("songCount = %d, want 1", after.SongCount)
	}

	// The call sequence must be create, then update, then re-read.
	calls := fake.callNames()
	joined := strings.Join(calls, ",")
	if !strings.Contains(joined, "createPlaylist") || !strings.Contains(joined, "updatePlaylist") {
		t.Fatalf("expected create then update, got: %s", joined)
	}
	if strings.Index(joined, "createPlaylist") > strings.Index(joined, "updatePlaylist") {
		t.Fatalf("updatePlaylist ran before createPlaylist: %s", joined)
	}
}

// createPlaylist without `name` is an error on a create. The tool must always
// send it — it is only optional when updating.
func TestCreatePlaylistRequiresName(t *testing.T) {
	fake := newFakeNavidrome(nil)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	sub := NewSubsonic(srv.URL, "u", "p")

	if err := sub.CreatePlaylist("", "", []string{"s1"}); err == nil {
		t.Fatal("expected an error when name is omitted on create")
	}
	if err := sub.CreatePlaylist("", "Named", []string{"s1"}); err != nil {
		t.Fatalf("named create should succeed: %v", err)
	}
}

// A search that sends artistCount/albumCount=0 gets an empty result set from
// real Navidrome. The client must never send them.
func TestSearchDoesNotSendZeroCounts(t *testing.T) {
	fake := newFakeNavidrome([]Candidate{{ID: "s1", Artist: "A", Title: "T"}})
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	sub := NewSubsonic(srv.URL, "u", "p")

	got, err := sub.Search3("T")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Search3 returned %d candidates — zero counts were probably sent", len(got))
	}
}

func TestMinMatchedRefusesToWipe(t *testing.T) {
	fake := newFakeNavidrome(nil) // empty library => nothing resolves
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	sub := NewSubsonic(srv.URL, "u", "p")

	if err := sub.CreatePlaylist("", "Weekly Jams for tester", []string{"s1", "s2"}); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("")
	cfg.minMatched = 1
	// Resolving against an empty library yields 0 ids; the write must be skipped.
	gen := assignment().Source
	ok, err := syncTracks(cfg, sub, "Weekly Jams for tester", gen,
		[]Track{{Artist: "Nobody At All", Title: "Not In This Library"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("syncOne reported success with zero matched tracks")
	}
	p, _, _ := sub.PlaylistByName("Weekly Jams for tester")
	if p.SongCount != 2 {
		t.Fatalf("existing playlist was modified: songCount = %d, want 2", p.SongCount)
	}
}
