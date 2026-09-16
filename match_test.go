package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The matcher is exercised entirely through testdata/matching.json. Every case
// in it is a behaviour observed against a live server, or a near-miss that
// would otherwise have shipped — the CJK empty-collapse and the same-title
// impostor were both found by hand-auditing live output hours apart, and both
// are caught here in milliseconds.
type fixture struct {
	Normalize []struct {
		Why   string `json:"why"`
		A     string `json:"a"`
		B     string `json:"b"`
		Equal bool   `json:"equal"`
	} `json:"normalize"`
	Resolve []struct {
		Why        string      `json:"why"`
		Want       Track       `json:"want"`
		Candidates []Candidate `json:"candidates"`
		ExpectID   *string     `json:"expect_id"`
	} `json:"resolve"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "matching.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.Normalize) == 0 || len(f.Resolve) == 0 {
		t.Fatal("fixture is empty — the tests would pass vacuously")
	}
	return f
}

func TestNormalize(t *testing.T) {
	f := loadFixture(t)
	for _, c := range f.Normalize {
		got := Normalize(c.A) == Normalize(c.B)
		if got != c.Equal {
			t.Errorf("Normalize(%q) == Normalize(%q) = %v, want %v  [%s]",
				c.A, c.B, got, c.Equal, c.Why)
		}
	}
}

// TestNormalizeNeverReturnsEmpty guards the specific regression behind the CJK
// case: if any non-empty input normalises to "", then every such input compares
// equal to every other and the matcher binds arbitrary tracks.
func TestNormalizeNeverReturnsEmpty(t *testing.T) {
	for _, s := range []string{"残響散歌", "朝が来る", "米津玄師", "♪", "---", "🎵"} {
		if got := Normalize(s); got == "" {
			t.Errorf("Normalize(%q) collapsed to the empty string", s)
		}
	}
}

func TestResolve(t *testing.T) {
	f := loadFixture(t)
	matched := 0
	for _, c := range f.Resolve {
		id, ok := Resolve(c.Want, c.Candidates)
		if c.ExpectID == nil {
			if ok {
				t.Errorf("Resolve(%q - %q) = %q, want no match  [%s]",
					c.Want.Artist, c.Want.Title, id, c.Why)
			}
			continue
		}
		if !ok {
			t.Errorf("Resolve(%q - %q) found nothing, want %q  [%s]",
				c.Want.Artist, c.Want.Title, *c.ExpectID, c.Why)
			continue
		}
		if id != *c.ExpectID {
			t.Errorf("Resolve(%q - %q) = %q, want %q  [%s]",
				c.Want.Artist, c.Want.Title, id, *c.ExpectID, c.Why)
			continue
		}
		matched++
	}
	// A fixture that only ever asserts "no match" would pass on a matcher that
	// never matches anything.
	if matched < 3 {
		t.Fatalf("only %d positive cases matched — the fixture no longer proves the matcher finds anything", matched)
	}
}
