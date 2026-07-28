package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every fixture below is invented. No account name, account id or address from a real deployment
// belongs in this repository, and a test is no exception to that.

// str writes a length-prefixed, null-terminated string the way the save files do.
func str(b *bytes.Buffer, s string) {
	binary.Write(b, binary.LittleEndian, int32(len(s)+1))
	b.WriteString(s)
	b.WriteByte(0)
}

// prop writes one property: name, type, size, index, payload.
func prop(b *bytes.Buffer, name, typ string, index int32, payload []byte) {
	str(b, name)
	str(b, typ)
	binary.Write(b, binary.LittleEndian, int32(len(payload)))
	binary.Write(b, binary.LittleEndian, index)
	b.Write(payload)
}

func strPayload(s string) []byte {
	var b bytes.Buffer
	str(&b, s)
	return b.Bytes()
}

func i32Payload(v int32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(v))
	return b
}

func u16Payload(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func f32Payload(v float32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, math.Float32bits(v))
	return b
}

// bytePayload is a ByteProperty whose enum is "None", which is how the per-stat numbers are stored.
func bytePropNone(b *bytes.Buffer, name string, index int32, value byte) {
	str(b, name)
	str(b, "ByteProperty")
	binary.Write(b, binary.LittleEndian, int32(1))
	binary.Write(b, binary.LittleEndian, index)
	str(b, "None")
	b.WriteByte(value)
}

// structProp wraps an inner property list, which is what the real files do for the character config
// and the stats.
func structProp(b *bytes.Buffer, name, structName string, inner []byte) {
	str(b, name)
	str(b, "StructProperty")
	binary.Write(b, binary.LittleEndian, int32(len(inner)))
	binary.Write(b, binary.LittleEndian, int32(0))
	str(b, structName)
	b.Write(inner)
}

// profileFixture builds a whole .arkprofile, header and all. The header is written to look like the
// real one: a class name and a table of plain strings back to back, which is exactly the shape the
// property search has to not mistake for a property.
type fixtureChar struct {
	account, name string
	extraLevel    *uint16
	xp            *float32
	engrams       *int32
	stats         map[int32]byte
	address       string
}

func profileFixture(t *testing.T, c fixtureChar) []byte {
	t.Helper()

	var stats bytes.Buffer
	if c.extraLevel != nil {
		prop(&stats, "CharacterStatusComponent_ExtraCharacterLevel", "UInt16Property", 0, u16Payload(*c.extraLevel))
	}
	if c.xp != nil {
		prop(&stats, "CharacterStatusComponent_ExperiencePoints", "FloatProperty", 0, f32Payload(*c.xp))
	}
	if c.engrams != nil {
		prop(&stats, "PlayerState_TotalEngramPoints", "IntProperty", 0, i32Payload(*c.engrams))
	}
	// An array in the middle, because the real file has them there and skipping one wrongly would
	// desynchronise everything after it.
	str(&stats, "PerMapExplorerNoteUnlocks")
	str(&stats, "ArrayProperty")
	binary.Write(&stats, binary.LittleEndian, int32(8))
	binary.Write(&stats, binary.LittleEndian, int32(0))
	str(&stats, "UInt32Property")
	stats.Write(make([]byte, 8))
	for index, points := range c.stats {
		bytePropNone(&stats, "CharacterStatusComponent_NumberOfLevelUpPointsApplied", index, points)
	}
	str(&stats, "None")

	var config bytes.Buffer
	prop(&config, "PlayerCharacterName", "StrProperty", 0, strPayload(c.name))
	str(&config, "None")

	var data bytes.Buffer
	if c.address != "" {
		prop(&data, "SavedNetworkAddress", "StrProperty", 0, strPayload(c.address))
	}
	prop(&data, "PlayerName", "StrProperty", 0, strPayload(c.account))
	structProp(&data, "MyPlayerCharacterConfig", "PrimalPlayerCharacterConfigStruct", config.Bytes())
	structProp(&data, "MyPersistentCharacterStats", "PrimalPersistentCharacterStatsStruct", stats.Bytes())
	str(&data, "None")

	var body bytes.Buffer
	prop(&body, "SavedPlayerDataVersion", "IntProperty", 0, i32Payload(9))
	structProp(&body, "MyData", "PrimalPlayerDataStruct", data.Bytes())
	str(&body, "None")

	var out bytes.Buffer
	out.Write(make([]byte, 24))
	str(&out, "PrimalPlayerDataBP_C")
	out.Write(make([]byte, 5))
	binary.Write(&out, binary.LittleEndian, int32(3))
	str(&out, "PrimalPlayerDataBP_C_2")
	str(&out, "ArkGameMode")
	str(&out, "PersistentLevel")
	out.Write(make([]byte, 13))
	binary.Write(&out, binary.LittleEndian, int32(out.Len()+8))
	binary.Write(&out, binary.LittleEndian, int32(0))
	out.Write(body.Bytes())
	return out.Bytes()
}

func u16p(v uint16) *uint16   { return &v }
func f32p(v float32) *float32 { return &v }
func i32p(v int32) *int32     { return &v }

func writeProfile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadCharacterReadsTheWholeProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "10000000000000001.arkprofile")
	writeProfile(t, path, profileFixture(t, fixtureChar{
		account: "TestKonto", name: "Testfigur",
		extraLevel: u16p(5), xp: f32p(197.16), engrams: i32p(40),
		stats:   map[int32]byte{0: 1, 7: 4},
		address: "203.0.113.7",
	}))

	c := readCharacter(path)
	if c.Err != "" {
		t.Fatalf("unexpected error: %s", c.Err)
	}
	if c.Account != "TestKonto" || c.Name != "Testfigur" {
		t.Errorf("account %q, name %q", c.Account, c.Name)
	}
	// Five extra levels is level six, and five spent points is what the levels bought. That the two
	// agree is what proved the reading of the real files.
	if c.Level != 6 {
		t.Errorf("level %d, want 6", c.Level)
	}
	if c.EngramPoints != 40 {
		t.Errorf("engram points %d", c.EngramPoints)
	}
	if int(c.XP) != 197 {
		t.Errorf("xp %f", c.XP)
	}
	total := 0
	for _, s := range c.Stats {
		total += int(s.Points)
	}
	if total != 5 {
		t.Errorf("%d points spent across %v, want 5", total, c.Stats)
	}
	// Sorted by name, so Gewicht comes before Leben.
	if len(c.Stats) != 2 || c.Stats[0].Stat != "Gewicht" || c.Stats[0].Points != 4 {
		t.Errorf("stats %v", c.Stats)
	}
}

// The one property whose absence is not a zero. A fresh character has no level property at all, and
// reading that as 0 would put every new character below the lowest level the game has.
func TestReadCharacterTreatsAMissingLevelAsOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "10000000000000002.arkprofile")
	writeProfile(t, path, profileFixture(t, fixtureChar{
		account: "TestKonto", name: "Frisch", xp: f32p(4.5),
	}))

	c := readCharacter(path)
	if c.Err != "" {
		t.Fatalf("unexpected error: %s", c.Err)
	}
	if c.Level != 1 {
		t.Errorf("level %d, want 1", c.Level)
	}
	if c.EngramPoints != 0 || len(c.Stats) != 0 {
		t.Errorf("a fresh character has no engram points and no spent stats: %d, %v", c.EngramPoints, c.Stats)
	}
}

// The address is in the file, right next to the account name. It must not come out of the reader at
// all, so that no page and no log can leak it by accident.
func TestReadCharacterNeverReturnsTheNetworkAddress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "10000000000000003.arkprofile")
	writeProfile(t, path, profileFixture(t, fixtureChar{
		account: "TestKonto", name: "Testfigur", address: "203.0.113.7",
	}))

	c := readCharacter(path)
	rendered := strings.Join([]string{c.Name, c.Account, c.File, c.Err}, " ")
	if strings.Contains(rendered, "203.0.113") {
		t.Errorf("the address reached the reader's output: %q", rendered)
	}
}

func TestReadCharacterReportsAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.arkprofile")
	writeProfile(t, path, []byte("this is not a save file at all"))

	c := readCharacter(path)
	if c.Err == "" {
		t.Fatal("a file that is not a profile has to be reported, not read as an empty character")
	}
	if c.File != "broken.arkprofile" {
		t.Errorf("the entry should still name its file, got %q", c.File)
	}
}

// A .profilebak is the same character's backup copy. Counting it would show every player twice.
func TestListCharactersSkipsBackupCopiesAndSurvivesABrokenFile(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	dir := filepath.Join(savedRoot(cfg), savedArksDir)
	good := profileFixture(t, fixtureChar{account: "TestKonto", name: "Testfigur", extraLevel: u16p(2)})
	writeProfile(t, filepath.Join(dir, "10000000000000001.arkprofile"), good)
	writeProfile(t, filepath.Join(dir, "10000000000000001.profilebak"), good)
	writeProfile(t, filepath.Join(dir, "10000000000000002.arkprofile"), []byte("kaputt"))

	chars, err := listCharacters(cfg, savedArksDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(chars) != 2 {
		t.Fatalf("%d characters, want the profile and the broken one but not the backup copy", len(chars))
	}
	var broken, ok int
	for _, c := range chars {
		if c.Err != "" {
			broken++
		} else {
			ok++
		}
	}
	if ok != 1 || broken != 1 {
		t.Errorf("%d readable and %d broken, want one of each", ok, broken)
	}
}

func TestListCharactersWithoutTheDirectory(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	chars, err := listCharacters(cfg, "nichtda")
	if err != nil || len(chars) != 0 {
		t.Fatalf("a missing save directory is not an error: %v, %v", err, chars)
	}
}

// The header in front of the property list is a class name and a table of plain strings back to
// back. The search for the list must not mistake two of those for a name and a type.
func TestFindPropsSkipsTheNameTableInTheHeader(t *testing.T) {
	b := profileFixture(t, fixtureChar{account: "TestKonto", name: "Testfigur"})
	off, ok := findProps(b)
	if !ok {
		t.Fatal("no property list found")
	}
	r := &reader{b: b, off: off}
	name, err := r.str()
	if err != nil || name != "SavedPlayerDataVersion" {
		t.Errorf("first property %q (%v), want the first real one", name, err)
	}
}

func TestStatNameFallsBackToTheIndex(t *testing.T) {
	if got := statName(7); got != "Gewicht" {
		t.Errorf("stat 7 is %q", got)
	}
	if got := statName(99); got != "Status 99" {
		t.Errorf("an unknown stat should name its index, got %q", got)
	}
}

// A tribe file has never been seen on this deployment, so the reader must not pretend. Anything it
// cannot name is reported rather than shown as an empty tribe.
func TestReadTribeReportsAFileItCannotName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1234567890.arktribe")
	writeProfile(t, path, profileFixture(t, fixtureChar{account: "TestKonto", name: "Testfigur"}))

	tr := readTribe(path)
	if tr.Err == "" {
		t.Error("a file without a tribe name has to say so instead of showing a nameless tribe")
	}
}
