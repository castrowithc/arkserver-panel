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

// A file that names no tribe is reported rather than shown as an empty tribe.
func TestReadTribeReportsAFileItCannotName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1234567890.arktribe")
	writeProfile(t, path, profileFixture(t, fixtureChar{account: "TestKonto", name: "Testfigur"}))

	tr := readTribe(path)
	if tr.Err == "" {
		t.Error("a file without a tribe name has to say so instead of showing a nameless tribe")
	}
}

// strArray writes an ArrayProperty of strings, the way the members are stored.
func strArray(b *bytes.Buffer, name string, values []string) {
	var inner bytes.Buffer
	binary.Write(&inner, binary.LittleEndian, int32(len(values)))
	for _, v := range values {
		str(&inner, v)
	}
	str(b, name)
	str(b, "ArrayProperty")
	binary.Write(b, binary.LittleEndian, int32(inner.Len()))
	binary.Write(b, binary.LittleEndian, int32(0))
	str(b, "StrProperty")
	b.Write(inner.Bytes())
}

// u32Array writes an ArrayProperty of 32-bit ids, the parallel list to the member names.
func u32Array(b *bytes.Buffer, name string, values []uint32) {
	var inner bytes.Buffer
	binary.Write(&inner, binary.LittleEndian, int32(len(values)))
	for _, v := range values {
		binary.Write(&inner, binary.LittleEndian, v)
	}
	str(b, name)
	str(b, "ArrayProperty")
	binary.Write(b, binary.LittleEndian, int32(inner.Len()))
	binary.Write(b, binary.LittleEndian, int32(0))
	str(b, "UInt32Property")
	b.Write(inner.Bytes())
}

// tribeFixture builds a whole .arktribe in the shape of the real one: a header, then a single
// TribeData structure carrying everything, including a tribe log whose entries must not be mistaken
// for members. All values are invented.
func tribeFixture(t *testing.T, name string, members []string, ids []uint32, owner uint32) []byte {
	t.Helper()

	var data bytes.Buffer
	prop(&data, "TribeName", "StrProperty", 0, strPayload(name))
	prop(&data, "OwnerPlayerDataID", "UInt32Property", 0, i32Payload(int32(owner)))
	prop(&data, "TribeId", "IntProperty", 0, i32Payload(42))
	strArray(&data, "MembersPlayerName", members)
	u32Array(&data, "MembersPlayerDataID", ids)
	strArray(&data, "TribeLog", []string{"Tag 1, 00:00:00: irgendetwas geschah"})
	str(&data, "None")

	var b bytes.Buffer
	// The header: a class name and a table of plain strings back to back, like the real file.
	binary.Write(&b, binary.LittleEndian, int32(1))
	binary.Write(&b, binary.LittleEndian, int32(1))
	b.Write(make([]byte, 16))
	str(&b, "PrimalTribeData")
	binary.Write(&b, binary.LittleEndian, int32(0))
	str(&b, "ArkGameMode")
	str(&b, "TheIsland")
	b.Write(make([]byte, 12))
	structProp(&b, "TribeData", "TribeData", data.Bytes())
	str(&b, "None")
	return b.Bytes()
}

// The defect this covers: the structure was entered under a name taken from the file header, which
// is not the struct type. It never matched, so the tribe stayed shut and every tribe read as
// unreadable.
func TestReadTribeReadsNameMembersAndFounder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1883561854.arktribe")
	writeProfile(t, path, tribeFixture(t,
		"Teststamm",
		[]string{"Erste Testfigur", "Zweite Testfigur"},
		[]uint32{111, 222},
		222,
	))

	tr := readTribe(path)
	if tr.Err != "" {
		t.Fatalf("readable tribe reported as broken: %s", tr.Err)
	}
	if tr.Name != "Teststamm" {
		t.Errorf("tribe name %q", tr.Name)
	}
	if strings.Join(tr.Members, ", ") != "Erste Testfigur, Zweite Testfigur" {
		t.Errorf("members %q", tr.Members)
	}
	// The founder is the member whose id matches, not simply the first one.
	if tr.Owner != "Zweite Testfigur" {
		t.Errorf("founder %q", tr.Owner)
	}
}

// A founder id that matches no member leaves the founder unnamed. Naming the first member instead
// would look right and be wrong.
func TestReadTribeLeavesAnUnmatchedFounderUnnamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1.arktribe")
	writeProfile(t, path, tribeFixture(t, "Teststamm", []string{"Erste Testfigur"}, []uint32{111}, 999))

	tr := readTribe(path)
	if tr.Err != "" {
		t.Fatalf("readable tribe reported as broken: %s", tr.Err)
	}
	if tr.Owner != "" {
		t.Errorf("founder %q, want none", tr.Owner)
	}
}

// The fixtures above are rebuilt from what a real file looks like, which is exactly the kind of
// agreement that can be wrong on both sides. Point ARK_TRIBE_FILE at a real .arktribe to check the
// reader against one; nothing from it is asserted on or printed, so no real name can end up in this
// repository or in a test log.
func TestReadTribeAgainstARealFile(t *testing.T) {
	path := os.Getenv("ARK_TRIBE_FILE")
	if path == "" {
		t.Skip("ARK_TRIBE_FILE not set")
	}
	tr := readTribe(path)
	if tr.Err != "" {
		t.Fatalf("real tribe file reported as broken: %s", tr.Err)
	}
	if tr.Name == "" {
		t.Error("no tribe name")
	}
	if len(tr.Members) == 0 {
		t.Error("no members")
	}
	if tr.Owner == "" {
		t.Error("no founder, so the id lists did not line up")
	}
	t.Logf("read a tribe: %d characters in the name, %d members, founder named", len(tr.Name), len(tr.Members))
}

// An array whose count cannot fit in the bytes the array declares is a misread, and must not size an
// allocation. The reader falls back to skipping the block.
func TestArrayWithAnImpossibleCountIsSkipped(t *testing.T) {
	var b bytes.Buffer
	str(&b, "MembersPlayerName")
	str(&b, "ArrayProperty")
	binary.Write(&b, binary.LittleEndian, int32(8))
	binary.Write(&b, binary.LittleEndian, int32(0))
	str(&b, "StrProperty")
	binary.Write(&b, binary.LittleEndian, int32(1<<30))
	b.Write(make([]byte, 4))
	str(&b, "None")

	p, err := parseProps(b.Bytes(), 0, nestedProfileStructs)
	if err != nil {
		t.Fatalf("the block should be skipped, not fail: %v", err)
	}
	if got := p.strs("MembersPlayerName"); got != nil {
		t.Errorf("a misread count produced %v", got)
	}
}
