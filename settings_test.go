package main

import (
	"strings"
	"testing"
)

// The catalogue is a fixed data file, so its size is a fact worth guarding: a field that silently
// disappears in some future edit would take a form field with it.
func TestCatalogueLoads(t *testing.T) {
	// 210 from the reference screens, plus the 36 per-level multipliers.
	if len(settingFields) != 246 {
		t.Fatalf("want 246 fields, got %d", len(settingFields))
	}
	if a, b := len(fieldsForFile("gameusersettings")), len(fieldsForFile("game")); a+b != len(settingFields) {
		t.Errorf("%d + %d fields do not add up to %d", a, b, len(settingFields))
	}

	byID := map[string]settingField{}
	for _, f := range settingFields {
		byID[f.ID] = f
	}
	xp, ok := byID["gameusersettings.xpmultiplier"]
	if !ok {
		t.Fatal("XPMultiplier is missing from the catalogue")
	}
	if xp.Section != "ServerSettings" || xp.Key != "XPMultiplier" || xp.Type != "number" {
		t.Errorf("XPMultiplier reads %+v", xp)
	}
	if mating, ok := byID["game.matingintervalmultiplier"]; !ok || mating.File != "game" {
		t.Errorf("a Game.ini field should target the Game.ini, got %+v", mating)
	}
}

// No field may point at a key the game writes more than once: those lines are beyond a form's
// reach, and the catalogue must not offer what the writer will refuse.
func TestNoCatalogueFieldCollidesWithARepeatedKey(t *testing.T) {
	f := parseINI(realINI(t))
	for _, field := range fieldsForFile("gameusersettings") {
		if _, n := f.lookup(field.Section, field.Key); n > 1 {
			t.Errorf("%s occurs %d times in the real file", field.ID, n)
		}
	}
}

func TestLoadSettingsRejectsABrokenCatalogue(t *testing.T) {
	cases := map[string]string{
		"duplicate id":  `[{"id":"a","label":"A","file":"game","section":"s","key":"k","type":"text"},{"id":"a","label":"B","file":"game","section":"s","key":"k2","type":"text"}]`,
		"unknown file":  `[{"id":"a","label":"A","file":"env","section":"s","key":"k","type":"text"}]`,
		"unknown type":  `[{"id":"a","label":"A","file":"game","section":"s","key":"k","type":"slider"}]`,
		"select w/o":    `[{"id":"a","label":"A","file":"game","section":"s","key":"k","type":"select"}]`,
		"missing key":   `[{"id":"a","label":"A","file":"game","section":"s","type":"text"}]`,
		"not even json": `nope`,
	}
	for name, data := range cases {
		if _, err := loadSettings([]byte(data)); err == nil {
			t.Errorf("%s should not load", name)
		}
	}
}

func TestNormalize(t *testing.T) {
	min, max := 0.0, 10.0
	number := settingField{Label: "n", Type: "number", Min: &min, Max: &max}
	check := settingField{Label: "c", Type: "checkbox"}
	sel := settingField{Label: "s", Type: "select", Options: []string{"true", "false"}}
	text := settingField{Label: "t", Type: "text"}

	ok := []struct {
		field settingField
		in    string
		want  string
	}{
		{check, " true ", "True"},
		{check, "1", "True"},
		{check, "false", "False"},
		{check, "0", "False"},
		{sel, "TRUE", "true"},
		{number, " 2.5 ", "2.5"},
		{number, "1.0", "1.0"}, // notation kept: 1.0 and 1 are the same value
		{number, "0", "0"},
		{number, "10", "10"},
		{text, "  Hallo Welt ", "Hallo Welt"},
	}
	for _, c := range ok {
		got, err := c.field.normalize(c.in)
		if err != nil {
			t.Errorf("%s %q: %v", c.field.Type, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s %q became %q, want %q", c.field.Type, c.in, got, c.want)
		}
	}

	bad := []struct {
		field settingField
		in    string
	}{
		{number, "11"},   // above the maximum
		{number, "-1"},   // below the minimum
		{number, "zwei"}, //
		{number, ""},
		{check, "vielleicht"},
		{sel, "maybe"},
		{text, "erste\nzweite"},
	}
	for _, c := range bad {
		if got, err := c.field.normalize(c.in); err == nil {
			t.Errorf("%s %q should have been rejected, became %q", c.field.Type, c.in, got)
		}
	}
}

// A browser sends "on" for a ticked box and nothing at all for an empty one. Translating that into
// a value is the handler's job, not this one's: here "on" is simply not a truth value, which keeps
// the rule "a field is what the catalogue says it is" intact.
func TestNormalizeLeavesTheBrowsersCheckboxWordToTheHandler(t *testing.T) {
	check := settingField{Label: "c", Type: "checkbox"}
	_, err := check.normalize("on")
	if err == nil || !strings.Contains(err.Error(), "Wahrheitswert") {
		t.Errorf("want a plain rejection, got %v", err)
	}
}
