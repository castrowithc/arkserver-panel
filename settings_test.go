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

// The reference values are carried so an empty field says something about the order of magnitude
// expected there. They are not defaults, and the two places where they would mislead must stay
// clear of them: a key the deployment owns cannot be edited anyway, and the catalogue itself
// records one field whose reference value is the name of another field rather than a value.
func TestReferenceValuesAreCarriedWhereTheyHelp(t *testing.T) {
	byKey := map[string]settingField{}
	withRef := 0
	for _, f := range settingFields {
		byKey[f.Key] = f
		if f.Ref != "" {
			withRef++
		}
	}
	if withRef != 239 {
		t.Errorf("want 239 fields with a reference value, got %d", withRef)
	}
	for key := range envManagedKeys {
		if f, ok := byKey[key]; ok && f.Ref != "" {
			t.Errorf("%s is owned by the deployment and read-only, so %q is noise", key, f.Ref)
		}
	}
	if f, ok := byKey["BadWordListURL"]; ok && f.Ref != "" {
		t.Errorf("the catalogue calls this reference value unusable, got %q", f.Ref)
	}
	if xp := byKey["XPMultiplier"]; xp.Ref == "" {
		t.Error("a plain rate field should carry its reference value")
	}
}

// Every field says where it has actually been seen, and the vocabulary is closed: a value outside
// it means the catalogue was edited by hand somewhere instead of derived from the files.
func TestEveryFieldRecordsWhereItWasSeen(t *testing.T) {
	seen := map[string]int{"eigene INI": 0, "Referenz-INI": 0, "nur Formular": 0}
	for _, f := range settingFields {
		if _, ok := seen[f.Proof]; !ok {
			t.Fatalf("%s records an unknown origin %q", f.ID, f.Proof)
		}
		seen[f.Proof]++
	}
	// The per-level multipliers were the one block no file attested. A reference server's Game.ini
	// carries all 36, so none of them may fall back to the form-only case.
	for _, f := range settingFields {
		if strings.HasPrefix(f.Key, "PerLevelStatsMultiplier_") && f.Proof == "nur Formular" {
			t.Errorf("%s is attested in a real Game.ini and should say so", f.Key)
		}
	}
	if seen["nur Formular"] != 17 {
		t.Errorf("want 17 fields seen only in a form, got %d", seen["nur Formular"])
	}
}
