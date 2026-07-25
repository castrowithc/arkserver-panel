package main

import (
	"os"
	"strings"
	"testing"
)

// realINI is a copy of the GameUserSettings.ini this server wrote itself, with the three credential
// values blanked. It is the interesting case: repeated keys, stock content overrides, several
// sections, 230 lines of material the settings form does not own.
func realINI(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/GameUserSettings.ini")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The gate for everything else: reading and writing back a file nobody edited must return the very
// same bytes. Whatever the parser does not understand, it must at least not destroy.
func TestRoundTripReturnsTheFileUnchanged(t *testing.T) {
	in := realINI(t)
	if got := parseINI(in).render(); got != in {
		t.Fatalf("round trip changed the file (%d bytes in, %d out)", len(in), len(got))
	}
}

func changedLines(before, after string) []string {
	b, a := strings.Split(before, "\n"), strings.Split(after, "\n")
	var diff []string
	for i := 0; i < len(a); i++ {
		if i >= len(b) || a[i] != b[i] {
			diff = append(diff, a[i])
		}
	}
	return diff
}

func TestSetChangesExactlyOneLine(t *testing.T) {
	in := realINI(t)
	f := parseINI(in)
	if err := f.set("ServerSettings", "XPMultiplier", "3.0"); err != nil {
		t.Fatal(err)
	}
	diff := changedLines(in, f.render())
	if len(diff) != 1 || diff[0] != "XPMultiplier=3.0" {
		t.Fatalf("want one changed line, got %q", diff)
	}
	if v, n := f.lookup("ServerSettings", "XPMultiplier"); v != "3.0" || n != 1 {
		t.Errorf("read back %q (%d occurrences)", v, n)
	}
}

// ARK repeats some keys on purpose, one line per entry. The panel cannot know which of them a form
// field would mean, so it refuses instead of picking one.
// The stock content overrides live in their own blueprint section, one line per crate.
const crateSection = "/Game/PrimalEarth/CoreBlueprints/TestGameMode.TestGameMode_C"

func TestSetRefusesARepeatedKey(t *testing.T) {
	f := parseINI(realINI(t))
	before := f.render()
	if _, n := f.lookup(crateSection, "ConfigOverrideSupplyCrateItems"); n < 2 {
		t.Fatalf("fixture should carry the repeated key, found %d", n)
	}
	if err := f.set(crateSection, "ConfigOverrideSupplyCrateItems", "x"); err == nil {
		t.Error("a repeated key must not be writable")
	}
	if f.render() != before {
		t.Error("the refused write still changed the file")
	}
}

func TestSetInsertsANewKeyIntoItsSection(t *testing.T) {
	in := "[ServerSettings]\nXPMultiplier=1.0\n\n[SessionSettings]\nSessionName=x\n"
	f := parseINI(in)
	if err := f.set("ServerSettings", "TamingSpeedMultiplier", "5"); err != nil {
		t.Fatal(err)
	}
	want := "[ServerSettings]\nXPMultiplier=1.0\nTamingSpeedMultiplier=5\n\n[SessionSettings]\nSessionName=x\n"
	if got := f.render(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// The empty Game.ini is the normal state until the first override, so the first value written has
// to bring its section along.
func TestSetCreatesTheSectionInAnEmptyFile(t *testing.T) {
	f := parseINI("")
	if err := f.set("/script/shootergame.shootergamemode", "MatingIntervalMultiplier", "0.5"); err != nil {
		t.Fatal(err)
	}
	want := "[/script/shootergame.shootergamemode]\nMatingIntervalMultiplier=0.5\n"
	if got := f.render(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetAppendsAMissingSectionAfterExistingContent(t *testing.T) {
	f := parseINI("[ServerSettings]\nXPMultiplier=1.0\n")
	if err := f.set("MessageOfTheDay", "Message", "Hallo"); err != nil {
		t.Fatal(err)
	}
	want := "[ServerSettings]\nXPMultiplier=1.0\n\n[MessageOfTheDay]\nMessage=Hallo\n"
	if got := f.render(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Back to "not set" means the line goes away. A value nobody chose has no business in the file.
func TestUnsetRemovesTheLine(t *testing.T) {
	in := realINI(t)
	f := parseINI(in)
	if err := f.unset("ServerSettings", "XPMultiplier"); err != nil {
		t.Fatal(err)
	}
	out := f.render()
	if _, n := f.lookup("ServerSettings", "XPMultiplier"); n != 0 {
		t.Error("the key survived")
	}
	if len(strings.Split(in, "\n"))-len(strings.Split(out, "\n")) != 1 {
		t.Error("more than one line disappeared")
	}
	if err := f.unset("ServerSettings", "XPMultiplier"); err != nil {
		t.Errorf("removing what is already gone is not an error: %v", err)
	}
}

func TestUnsetRefusesARepeatedKey(t *testing.T) {
	f := parseINI(realINI(t))
	if err := f.unset(crateSection, "ConfigOverrideSupplyCrateItems"); err == nil {
		t.Error("a repeated key must not be removable either")
	}
}

// The game writes sections in one spelling, the field catalogue carries another (Game.ini uses
// lower case throughout). Both must find the same block.
func TestSectionAndKeyMatchingIgnoresCase(t *testing.T) {
	f := parseINI("[/Script/ShooterGame.ShooterGameMode]\nMatingIntervalMultiplier=1\n")
	if v, n := f.lookup("/script/shootergame.shootergamemode", "matingintervalmultiplier"); v != "1" || n != 1 {
		t.Fatalf("got %q (%d occurrences)", v, n)
	}
	if err := f.set("/script/shootergame.shootergamemode", "MatingIntervalMultiplier", "2"); err != nil {
		t.Fatal(err)
	}
	// The spelling in the file wins: the panel corrects values, not the operator's file.
	if got := f.render(); got != "[/Script/ShooterGame.ShooterGameMode]\nMatingIntervalMultiplier=2\n" {
		t.Errorf("got %q", got)
	}
}

func TestCommentsAndUnknownContentSurvive(t *testing.T) {
	in := "; ein Kommentar\n[ServerSettings]\n# noch einer\nXPMultiplier=1.0\nUnbekannterKey=42\n\n[Fremd]\na=b\n"
	f := parseINI(in)
	if err := f.set("ServerSettings", "XPMultiplier", "2.0"); err != nil {
		t.Fatal(err)
	}
	out := f.render()
	for _, keep := range []string{"; ein Kommentar", "# noch einer", "UnbekannterKey=42", "[Fremd]", "a=b"} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q did not survive:\n%s", keep, out)
		}
	}
}

// A value may contain an equals sign of its own; only the first one separates key from value.
func TestValuesKeepTheirOwnEqualsSigns(t *testing.T) {
	f := parseINI("[ServerSettings]\nCustomDynamicConfigUrl=http://example.invalid/?a=b\n")
	if v, _ := f.lookup("ServerSettings", "CustomDynamicConfigUrl"); v != "http://example.invalid/?a=b" {
		t.Errorf("got %q", v)
	}
	if err := f.set("ServerSettings", "CustomDynamicConfigUrl", "http://example.invalid/?c=d"); err != nil {
		t.Fatal(err)
	}
	if got := f.render(); !strings.Contains(got, "CustomDynamicConfigUrl=http://example.invalid/?c=d") {
		t.Errorf("got %q", got)
	}
}

func TestFileWithoutFinalNewlineGainsOneOnlyWhenWritten(t *testing.T) {
	in := "[ServerSettings]\nXPMultiplier=1.0"
	if got := parseINI(in).render(); got != in {
		t.Errorf("an untouched file must stay as it is, got %q", got)
	}
	f := parseINI(in)
	if err := f.set("ServerSettings", "XPMultiplier", "2.0"); err != nil {
		t.Fatal(err)
	}
	if got := f.render(); got != "[ServerSettings]\nXPMultiplier=2.0\n" {
		t.Errorf("got %q", got)
	}
}

// Carriage returns are not this layer's business: it hands back what it read, so a file written on
// Windows does not silently become a different file here.
func TestCarriageReturnsSurviveTheParser(t *testing.T) {
	in := "[ServerSettings]\r\nXPMultiplier=1.0\r\n"
	f := parseINI(in)
	if got := f.render(); got != in {
		t.Errorf("got %q", got)
	}
	if v, n := f.lookup("ServerSettings", "XPMultiplier"); v != "1.0" || n != 1 {
		t.Errorf("got %q (%d occurrences)", v, n)
	}
}
