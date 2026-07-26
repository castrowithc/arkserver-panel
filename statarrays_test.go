package main

import (
	"strings"
	"testing"
)

const statSection = "/script/shootergame.shootergamemode"

// The whole block rests on this: an index makes a key of its own. The writer refuses a key that
// occurs more than once, and if PerLevelStatsMultiplier_Player[0] and [1] counted as the same key,
// all twelve fields of a block would lock each other out on the second value written.
func TestIndexedKeysAreDistinctKeys(t *testing.T) {
	f := parseINI("[" + statSection + "]\nPerLevelStatsMultiplier_Player[0]=1.5\nPerLevelStatsMultiplier_Player[1]=2.0\n")

	for key, want := range map[string]string{
		"PerLevelStatsMultiplier_Player[0]": "1.5",
		"PerLevelStatsMultiplier_Player[1]": "2.0",
	} {
		value, occurrences := f.lookup(statSection, key)
		if occurrences != 1 {
			t.Errorf("%s occurs %d times, want 1", key, occurrences)
		}
		if value != want {
			t.Errorf("%s is %q, want %q", key, value, want)
		}
	}

	// And a write to one index leaves the other alone.
	if err := f.set(statSection, "PerLevelStatsMultiplier_Player[0]", "3.0"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, _ := f.lookup(statSection, "PerLevelStatsMultiplier_Player[1]"); v != "2.0" {
		t.Errorf("the neighbouring index changed to %q", v)
	}
}

// The empty Game.ini is the normal starting state, so the first multiplier written has to bring the
// section with it.
func TestFirstStatValueBringsItsSection(t *testing.T) {
	f := parseINI("")
	if err := f.set(statSection, "PerLevelStatsMultiplier_DinoTamed[8]", "0.17"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := f.render()
	if !strings.Contains(got, "["+statSection+"]") {
		t.Errorf("the section is missing:\n%s", got)
	}
	if !strings.Contains(got, "PerLevelStatsMultiplier_DinoTamed[8]=0.17") {
		t.Errorf("the value is missing:\n%s", got)
	}

	// Clearing it takes the line away again, and the header the write brought along goes with it:
	// the panel must not leave behind a block nobody wrote.
	if err := f.unset(statSection, "PerLevelStatsMultiplier_DinoTamed[8]"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if got := strings.TrimSpace(f.render()); got != "" {
		t.Errorf("the file should be empty again, got:\n%s", got)
	}
}

func TestEmptySectionSurvivesWhenItIsNotOurs(t *testing.T) {
	t.Run("a comment keeps its section", func(t *testing.T) {
		f := parseINI("[" + statSection + "]\n; von Hand notiert\nPerLevelStatsMultiplier_Player[0]=1.5\n")
		if err := f.unset(statSection, "PerLevelStatsMultiplier_Player[0]"); err != nil {
			t.Fatal(err)
		}
		got := f.render()
		if !strings.Contains(got, "["+statSection+"]") || !strings.Contains(got, "von Hand notiert") {
			t.Errorf("the comment lost its section:\n%s", got)
		}
	})

	t.Run("a remaining key keeps its section", func(t *testing.T) {
		f := parseINI("[" + statSection + "]\nPerLevelStatsMultiplier_Player[0]=1.5\nMatingIntervalMultiplier=0.5\n")
		if err := f.unset(statSection, "PerLevelStatsMultiplier_Player[0]"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.render(), "["+statSection+"]") {
			t.Errorf("the section went although a key is left:\n%s", f.render())
		}
	})

	t.Run("the next section is left alone", func(t *testing.T) {
		f := parseINI("[" + statSection + "]\nPerLevelStatsMultiplier_Player[0]=1.5\n\n[ServerSettings]\nXPMultiplier=2\n")
		if err := f.unset(statSection, "PerLevelStatsMultiplier_Player[0]"); err != nil {
			t.Fatal(err)
		}
		got := f.render()
		if strings.Contains(got, statSection) {
			t.Errorf("the emptied section stayed:\n%s", got)
		}
		if !strings.Contains(got, "[ServerSettings]") || !strings.Contains(got, "XPMultiplier=2") {
			t.Errorf("the following section was damaged:\n%s", got)
		}
	})
}

func TestCatalogueCarriesTheThirtySixStatFields(t *testing.T) {
	byGroup := map[string]int{}
	for _, f := range settingFields {
		if !strings.HasPrefix(f.Key, "PerLevelStatsMultiplier_") {
			continue
		}
		byGroup[f.Group]++
		if f.File != "game" || f.Section != statSection {
			t.Errorf("%s targets %s/%s", f.ID, f.File, f.Section)
		}
		if f.Type != "number" {
			t.Errorf("%s has type %q, want number", f.ID, f.Type)
		}
		// The source gives no upper bound, so the catalogue must not invent one.
		if f.Max != nil {
			t.Errorf("%s carries a maximum the source does not give", f.ID)
		}
		if f.Min == nil || *f.Min != 0 {
			t.Errorf("%s should be bounded below at 0", f.ID)
		}
	}
	for _, group := range []string{"Multiplikatoren Spieler", "Multiplikatoren wilde Dinos", "Multiplikatoren gezähmte Dinos"} {
		if byGroup[group] != 12 {
			t.Errorf("group %q has %d fields, want 12", group, byGroup[group])
		}
	}
}

// The reservation is the price of shipping keys nobody could attest here, so it has to reach the
// page. A block without it would present those fields like any other.
func TestStatBlocksCarryTheReservation(t *testing.T) {
	groups := buildGroups(map[string]*iniSource{
		"game":             {entry: fileEntry{ID: "game"}, ini: parseINI("")},
		"gameusersettings": {entry: fileEntry{ID: "gameusersettings"}, ini: parseINI("")},
	})

	seen := 0
	for _, g := range groups {
		hasStats := strings.HasPrefix(g.Rows[0].Key, "PerLevelStatsMultiplier_")
		switch {
		case hasStats && g.Note == "":
			t.Errorf("group %q carries stat arrays but no reservation", g.Name)
		case hasStats:
			seen++
		case g.Note != "":
			t.Errorf("group %q carries a reservation it should not have", g.Name)
		}
	}
	if seen != 3 {
		t.Errorf("found %d stat blocks, want 3", seen)
	}
}
