// The field catalogue behind the generated settings form: which key a form field writes, into which
// file and section, and what counts as a valid value. It was compiled from the field inventory of a
// reference host's configuration screens and checked against the INIs this server writes itself, so
// the panel does not have to guess a key at runtime.
//
// The per-level stat arrays are the one part whose key names no source here attests: they are
// indexed keys taken from knowledge of the game, and this deployment cannot attest them either,
// because ARK leaves the Game.ini empty until an override exists and ignores an unknown key without
// a word. They are carried anyway, with the reservation stated on their blocks, so a value that
// does nothing is not mistaken for a setting that was made.
//
// Deliberately absent: the procedural map parameters, which are moot for this deployment.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

//go:embed settings.json
var settingsJSON []byte

type settingField struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	File    string   `json:"file"` // matches the ids in configFiles
	Section string   `json:"section"`
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Step    *float64 `json:"step,omitempty"`
	Options []string `json:"options,omitempty"`
	Group   string   `json:"group"`
	// Proof records whether the key also occurs in this server's own INI or only in the reference
	// export. It carries no behaviour, it keeps the origin of a field answerable.
	Proof string `json:"proof"`
}

// settingFields is parsed at start-up rather than on first use: a catalogue that does not load is a
// packaging error, and it should surface when the binary starts, not when someone opens a page.
var settingFields = mustLoadSettings(settingsJSON)

func mustLoadSettings(data []byte) []settingField {
	fields, err := loadSettings(data)
	if err != nil {
		panic(err)
	}
	return fields
}

func loadSettings(data []byte) ([]settingField, error) {
	var fields []settingField
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("settings catalogue: %w", err)
	}
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		switch {
		case f.ID == "" || f.Key == "" || f.Section == "" || f.Label == "":
			return nil, fmt.Errorf("settings catalogue: incomplete field %+v", f)
		case f.File != "gameusersettings" && f.File != "game":
			return nil, fmt.Errorf("settings catalogue: field %s targets unknown file %q", f.ID, f.File)
		case seen[f.ID]:
			return nil, fmt.Errorf("settings catalogue: duplicate id %s", f.ID)
		}
		switch f.Type {
		case "checkbox", "text":
		case "number":
		case "select":
			if len(f.Options) == 0 {
				return nil, fmt.Errorf("settings catalogue: field %s is a select without options", f.ID)
			}
		default:
			return nil, fmt.Errorf("settings catalogue: field %s has unknown type %q", f.ID, f.Type)
		}
		seen[f.ID] = true
	}
	return fields, nil
}

func fieldsForFile(id string) []settingField {
	var out []settingField
	for _, f := range settingFields {
		if f.File == id {
			out = append(out, f)
		}
	}
	return out
}

// normalize turns what a form sent into the exact text the file should carry, and rejects what the
// catalogue does not allow. The game reads True/False capitalised and a dot as the decimal point;
// beyond that a number keeps the notation the operator typed, because 1.0 and 1 are the same value
// and rewriting one into the other would show up as a change nobody made.
func (f settingField) normalize(value string) (string, error) {
	v := strings.TrimSpace(value)
	switch f.Type {
	case "checkbox":
		b, err := strconv.ParseBool(v)
		if err != nil {
			return "", fmt.Errorf("%s: %q ist kein Wahrheitswert", f.Label, value)
		}
		if b {
			return "True", nil
		}
		return "False", nil
	case "select":
		for _, o := range f.Options {
			if strings.EqualFold(o, v) {
				return o, nil
			}
		}
		return "", fmt.Errorf("%s: %q ist keine der Optionen %s", f.Label, value, strings.Join(f.Options, ", "))
	case "number":
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return "", fmt.Errorf("%s: %q ist keine Zahl", f.Label, value)
		}
		if f.Min != nil && n < *f.Min {
			return "", fmt.Errorf("%s: %v liegt unter dem Minimum %v", f.Label, n, *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return "", fmt.Errorf("%s: %v liegt ueber dem Maximum %v", f.Label, n, *f.Max)
		}
		return v, nil
	default: // text
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("%s: mehrzeilige Werte kann die INI nicht tragen", f.Label)
		}
		return v, nil
	}
}
