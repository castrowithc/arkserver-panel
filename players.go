// Who is in a save game: the characters from its .arkprofile files and the tribes from its
// .arktribe files. Read only. Characters and tribes are made and unmade in the game, and nothing
// here changes that.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// character is one player's character in one save game.
type character struct {
	// Name is the character's name in the game, Account the name of the account behind it. They are
	// different things and both are worth showing: the account is who to talk to, the character is
	// what is in the world.
	Name    string
	Account string
	// Level is the character's level, which is one plus the extra levels the file counts. A
	// character that never levelled carries no such property at all, and the answer is then 1 rather
	// than 0.
	Level int64
	// XP and EngramPoints are shown as the file has them. Both are absent on a fresh character.
	XP           float64
	EngramPoints int64
	// Stats are the level-up points spent, by stat name, and only for the stats that have any. A
	// stat with no points is not in the file and does not belong in the list either.
	Stats []statPoints
	// File is the file this came from, so a character can be found on disk.
	File string
	// Err is set when the file could not be read. Then nothing else on this entry means anything,
	// and the page says so instead of showing an empty character.
	Err string
}

type statPoints struct {
	Stat   string
	Points int64
}

// tribe is one tribe in one save game. Unlike the characters this is not measured against a real
// file: no deployment here has ever had a tribe. Everything it reads is therefore optional, and a
// file that does not answer is reported as unreadable rather than shown as an empty tribe.
type tribe struct {
	Name    string
	Owner   string
	Members []string
	File    string
	Err     string
}

// arkStats names the level-up stats by the index the file stores them under. The order is the
// game's own and is what makes the numbers mean anything: without it the page could only print an
// index.
var arkStats = []string{
	"Leben", "Ausdauer", "Betäubung", "Sauerstoff", "Nahrung", "Wasser",
	"Temperatur", "Gewicht", "Nahkampfschaden", "Bewegungstempo", "Widerstand", "Herstellungstempo",
}

// nestedProfileStructs are the only structures worth stepping into. Everything else in a profile is
// colours, bone sliders and item lists, which are bytes to skip rather than a property list.
var nestedProfileStructs = map[string]bool{
	"PrimalPlayerDataStruct":               true,
	"PrimalPlayerCharacterConfigStruct":    true,
	"PrimalPersistentCharacterStatsStruct": true,
	"PrimalTribeData":                      true,
}

// listCharacters reads every profile in one save directory. A file that cannot be read becomes an
// entry that says so: it exists, the operator should know it is there, and hiding it would make a
// broken profile look like a save game with one player fewer.
func listCharacters(cfg config, saveDir string) ([]character, error) {
	paths, err := saveFiles(cfg, saveDir, ".arkprofile")
	if err != nil {
		return nil, err
	}
	out := make([]character, 0, len(paths))
	for _, path := range paths {
		out = append(out, readCharacter(path))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readCharacter(path string) character {
	c := character{File: filepath.Base(path)}
	b, err := os.ReadFile(path)
	if err != nil {
		c.Err = err.Error()
		return c
	}
	root, err := readProps(b, nestedProfileStructs)
	if err != nil {
		c.Err = err.Error()
		return c
	}
	data, ok := root.sub("MyData")
	if !ok {
		c.Err = "die Datei enthält keine Spielerdaten"
		return c
	}

	c.Account, _ = data.str("PlayerName")
	if config, ok := data.sub("MyPlayerCharacterConfig"); ok {
		c.Name, _ = config.str("PlayerCharacterName")
	}
	// The network address sits right beside the account name and is deliberately not read. It says
	// nothing about a save game and everything about a person.

	// Absent means default, throughout. A character at level 1 has no level property, no experience
	// and no engram points, and reporting those as zero would be wrong about the level.
	c.Level = 1
	stats, ok := data.sub("MyPersistentCharacterStats")
	if !ok {
		return c
	}
	if extra, ok := stats.num("CharacterStatusComponent_ExtraCharacterLevel"); ok {
		c.Level = extra + 1
	}
	c.XP, _ = stats.float("CharacterStatusComponent_ExperiencePoints")
	c.EngramPoints, _ = stats.num("PlayerState_TotalEngramPoints")
	for index, points := range stats.indexed("CharacterStatusComponent_NumberOfLevelUpPointsApplied") {
		if points == 0 {
			continue
		}
		c.Stats = append(c.Stats, statPoints{Stat: statName(index), Points: points})
	}
	sort.Slice(c.Stats, func(i, j int) bool { return c.Stats[i].Stat < c.Stats[j].Stat })
	return c
}

// statName turns the file's index into the stat it stands for. An index the list does not cover is
// named as itself: the game could add one, and a number is a better answer than a wrong word.
func statName(index int32) string {
	if int(index) < len(arkStats) && index >= 0 {
		return arkStats[index]
	}
	return fmt.Sprintf("Status %d", index)
}

// listTribes reads every tribe file in one save directory. Unverified against a real file, which is
// why every field is optional and a file that yields no name at all counts as unreadable.
func listTribes(cfg config, saveDir string) ([]tribe, error) {
	paths, err := saveFiles(cfg, saveDir, ".arktribe")
	if err != nil {
		return nil, err
	}
	out := make([]tribe, 0, len(paths))
	for _, path := range paths {
		out = append(out, readTribe(path))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readTribe(path string) tribe {
	t := tribe{File: filepath.Base(path)}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Err = err.Error()
		return t
	}
	root, err := readProps(b, nestedProfileStructs)
	if err != nil {
		t.Err = err.Error()
		return t
	}
	// The tribe data may sit one level down, the way a profile's does, or at the top. Both are
	// tried rather than assumed, because which one it is has never been measured here.
	data := root
	if sub, ok := root.sub("TribeData"); ok {
		data = sub
	}
	t.Name, _ = data.str("TribeName")
	t.Owner, _ = data.str("OwnerPlayerName")
	for _, key := range data.order {
		if key.Name != "MembersPlayerName" {
			continue
		}
		if name, ok := data.values[key].(string); ok && name != "" {
			t.Members = append(t.Members, name)
		}
	}
	if t.Name == "" {
		t.Err = "die Datei nennt keinen Stammnamen; das Format weicht von dem der Profile ab"
	}
	return t
}

// saveFiles lists one kind of file in a save directory, newest name order aside. The .profilebak
// beside a profile is its backup copy and is skipped: it is the same character, not a second one.
func saveFiles(cfg config, saveDir, ext string) ([]string, error) {
	dir := filepath.Join(savedRoot(cfg), saveDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ext) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}
