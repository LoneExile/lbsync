# lbsync

Sync your **[ListenBrainz](https://listenbrainz.org) generated playlists** into
any **Subsonic** server — Navidrome, Airsonic, Gonic, … — so they show up in the
clients you actually listen with: Feishin, Symfonium, Amperfy, DSub, Sonixd.

ListenBrainz builds *Weekly Jams*, *Weekly Exploration* and *Daily Jams* from
your listening history, but they only exist on the website. Subsonic clients can
only see what is in your own server. `lbsync` is the bridge: it pulls each
generated playlist and writes it in as a real playlist, **updated in place**
every time it runs.

```
ListenBrainz offers for 'you': weekly-exploration, weekly-jams

  Weekly Jams
    source : Weekly Jams for you, week of 2026-09-14 Mon  (2026-09-14T00:08:16)
    tracks : 50 on ListenBrainz
    owned  : 6 of 50 (not in library: 44)
    target : 'Weekly Jams for you' (id 0LLKTDaq..., 6 tracks)
    OK: 'Weekly Jams for you' now has 6 tracks (public)

  Weekly Exploration
    source : Weekly Exploration for you, week of 2026-09-14 Mon
    tracks : 50 on ListenBrainz
    owned  : 5 of 50 (not in library: 45)
    OK: 'Weekly Exploration for you' now has 5 tracks (public)
```

## Expect a short playlist

This is the most surprising thing about the tool, so it is up front: **the
result is usually much shorter than what ListenBrainz shows.** ListenBrainz
draws on your *all-time* listening history — including services you used years
before this library existed — while a Subsonic library is whatever files you
own. Unmatched tracks are dropped, never downloaded.

A 50-track ListenBrainz playlist landing as 5–15 tracks is normal and correct.

## Usage

```sh
docker run --rm \
  -e SUBSONIC_URL=http://navidrome:4533 \
  -e SUBSONIC_USER=admin \
  -e SUBSONIC_PASSWORD=secret \
  -e LISTENBRAINZ_USER=you \
  ghcr.io/loneexile/lbsync:latest
```

Run it weekly — a cron job, a systemd timer, or a Kubernetes CronJob:

```cron
30 5 * * 1  docker run --rm --env-file /etc/lbsync.env ghcr.io/loneexile/lbsync:latest
```

`examples/docker-compose.yml` has a compose version. There is also a single
static binary with no dependencies if you would rather not use a container:

```sh
CGO_ENABLED=0 go build -o lbsync . && ./lbsync
```

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `SUBSONIC_URL` | — | **Required.** Server base URL, e.g. `http://navidrome:4533` |
| `SUBSONIC_USER` | — | **Required.** User to authenticate as **and to own the playlists** |
| `SUBSONIC_PASSWORD` | — | **Required.** Password. An `enc:<hex>` value is passed through as-is |
| `LISTENBRAINZ_USER` | — | **Required.** ListenBrainz username |
| `PLAYLISTS` | all offered | Comma-separated types to sync, e.g. `weekly-jams,weekly-exploration` |
| `PLAYLIST_PUBLIC` | `true` | Make playlists visible to all users of the server |
| `DRY_RUN` | `false` | Resolve and report without writing anything |
| `MIN_MATCHED` | `1` | Refuse to write below this many tracks (see *Safety*) |
| `REJECTIONS_PATH` | — | Write unmatched tracks as a JSON worklist to this path |

Run with `DRY_RUN=true` first to see what would happen.

## The rejection worklist

Every track ListenBrainz recommends that is *not* in your library is a track you
have listened to (or been recommended) but do not own. That makes the rejections
an **acquisition list**, so they are a first-class output rather than a log line:

```sh
REJECTIONS_PATH=/out/rejections.json
```

```json
{
  "generated_at": "2026-09-16T07:24:43Z",
  "listenbrainz_user": "you",
  "playlist": "Weekly Jams for you",
  "source": "Weekly Jams for you, week of 2026-09-14 Mon",
  "requested": 50,
  "matched": 6,
  "not_in_library": [
    {
      "artist": "The Rare Occasions",
      "title": "Notion",
      "album": "Attaboy",
      "recording_mbid": "4dd0d33d-..."
    }
  ]
}
```

Feed it to whatever acquires music for you — Lidarr, a Soulseek client, a
shopping list. `recording_mbid` is included for services that can look tracks up
by MusicBrainz ID; it is never used for matching (see below).

## Safety

The tool will not silently destroy a playlist:

- **It never wipes a playlist with nothing.** If zero tracks resolve, it skips,
  says why, and exits non-zero — a failure upstream must not empty a working
  playlist.
- **It verifies observed state, never requested state.** After writing, it
  re-reads the playlist and fails if the track count or the visibility flag is
  not what it intended. Both checks exist because both were real bugs.
- `MIN_MATCHED` raises the floor for a harder guarantee. Leave it at `1` unless
  you know your library's coverage — a high floor makes the job fail on
  legitimately sparse weeks.

## Design notes

Everything below was learned against a live server. Each one silently produces
*wrong* results rather than errors, which is why they are documented here and
covered by tests.

**Tracks are matched by artist + title, not by MusicBrainz ID.** Subsonic's API
does not expose recording MBIDs, so ID matching cannot work against a real
library.

**Both the title and the artist must match — there is no fallback.** An earlier
version fell back to a same-title candidate when the artist filter came up
empty. Measured on a real 50-track ListenBrainz playlist, strict matching
resolved **6** tracks and the fallback reported **16** — the extra 10 were wrong
songs. It substituted *Kings of Leon – Notion* for *The Rare Occasions – Notion*,
which the library does not contain. A short playlist of real tracks beats a long
one full of impostors. (The 6→16 difference came entirely from the fallback, not
from a change of query — both query forms resolve the same 6.)

**Search by title alone, then filter by artist.** The query is the title (a
broader candidate pool); precision comes from `Matches`, not from the query.

**Never send `artistCount=0` / `albumCount=0`.** Navidrome returns an empty
`searchResult3` for *every* query when either is zero, which looks exactly like
an empty library. They are omitted entirely.

**Normalisation must never produce an empty string.** Stripping punctuation from
a CJK title leaves nothing, and `"" == ""` makes any two unrelated CJK tracks
compare equal — binding whichever the server returned first. CJK falls back to
the raw lowercased string, so only genuinely identical titles match.

**`createPlaylist` requires `name`, and ignores `public`.** `name` is required
when creating and merely tolerated when updating, so it is always sent.
`public` is ignored on create — a new playlist comes back private no matter what
was requested — so `updatePlaylist` sets it, and the result is read back. A
private playlist owned by the configured user is invisible to every other user,
which would defeat the point of the tool.

## Tests

The matcher is the part that can be wrong without failing, so it is isolated and
driven by a golden fixture:

```sh
go test ./...
```

`testdata/matching.json` is the documented contract: every case in it is either
a behaviour observed against a live server or a near-miss that would otherwise
have shipped — the CJK collapse and the same-title impostor were both found by
hand-auditing live output hours apart, and both are caught here in milliseconds.

## Limitations

- Only **generated** playlists (`Created for you`). Hand-made ListenBrainz
  playlists are not synced.
- **One playlist per type, updated in place** — the newest Weekly Jams replaces
  last week's. ListenBrainz expires older entries (~2 weeks) and drops them from
  its API entirely, so a per-week mirror would need a deliberate decision about
  what happens to the Navidrome copies when a source disappears. Not implemented
  on purpose.
- Unmatched tracks are dropped; nothing is downloaded (but see the worklist).
- Playlists are owned by `SUBSONIC_USER`, so keep `PLAYLIST_PUBLIC=true` unless
  that account is the only one you use.

## License

MIT — see [LICENSE](LICENSE).
