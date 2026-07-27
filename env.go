// The deployment's .env, read-only and with names instead of raw lines.
//
// These values are the ones an operator most often wants: the server's name in the browser, its
// passwords, the number of slots, the mods. They are not in the settings form and cannot be, because
// this deployment holds them in the .env: some are written into GameUserSettings.ini by arkmanager at
// every start, the rest are launch flags or lifecycle switches read when the container is built. An
// edit in the form would survive until the next start and then vanish without a word.
//
// So the page shows them, says who owns each one, and says where to change it. Writing them is
// deliberately not offered: applying a change means recreating the container on the host, and
// whoever is standing there has the file open already.
package main

import (
	"os"
	"path/filepath"
	"strings"
)

type envValue struct {
	Key   string
	Label string
	// Effect says what the value does in this deployment, which is also why it is not in the form.
	Effect string
	Value  string
	// Secret keeps a password off the screen; Set then still says whether one is configured.
	Secret bool
	Set    bool
	// Overruled names the value that actually applies and where it comes from, for a key that
	// something else has taken over. Empty means the value on the left is the one in force. Without
	// this the page would show a value that is no longer true and look right doing it.
	Overruled string
}

// envShown is what the page lists, in reading order. Structural keys (the volume, the ids, the
// memory cap) are deliberately absent: they belong to the host, not to the game session, and the raw
// file is on the files page for anyone who needs them.
var envShown = []struct{ key, label, effect string }{
	{"SESSION_NAME", "Servername", "steht als SessionName in der GameUserSettings.ini, danach in der Serverliste"},
	{"SERVER_MAP", "Karte", "welche Welt geladen wird"},
	{"MAX_PLAYERS", "Slots", "steht als MaxPlayers in der GameUserSettings.ini"},
	{"SERVER_PASSWORD", "Beitrittspasswort", "steht als ServerPassword in der GameUserSettings.ini; leer heißt offener Server"},
	{"ADMIN_PASSWORD", "Admin-Passwort", "steht als ServerAdminPassword in der GameUserSettings.ini, zugleich das RCON-Passwort des Panels"},
	{"GAME_MOD_IDS", "Mod-IDs", "steht als GameModIds in der GameUserSettings.ini"},
	{"SERVER_MAP_MOD_ID", "Karten-Mod", "Workshop-ID einer Mod-Karte statt einer Standardkarte"},
	{"UPDATE_ON_START", "Beim Start aktualisieren", "Lifecycle: sucht beim Hochfahren nach einem Update"},
	{"BACKUP_ON_STOP", "Beim Stoppen sichern", "Lifecycle: schreibt vor dem Herunterfahren eine Sicherung"},
	{"PRE_UPDATE_BACKUP", "Vor dem Update sichern", "Lifecycle"},
	{"WARN_ON_STOP", "Beim Stoppen warnen", "Lifecycle: kündigt das Herunterfahren im Spiel an"},
	{"DISABLE_BATTLEYE", "BattlEye abgeschaltet", "Startflag -NoBattlEye"},
	{"ENABLE_CROSSPLAY", "Crossplay", "Startflag -crossplay"},
	{"ENABLE_GAME_LOG", "Spiel-Log", "Startflag -servergamelog"},
}

func envPath(cfg config) string { return filepath.Join(cfg.envDir, ".env") }

func isSecretEnvKey(key string) bool { return isSecretKey(key) }

// loadEnvValues reads the file each time rather than caching: it is small, and a stale answer to
// "what is this server running with" is worse than no answer.
func loadEnvValues(cfg config) ([]envValue, error) {
	content, err := readTextFile(envPath(cfg))
	if err != nil {
		return nil, err
	}
	file := parseINI(content)

	out := make([]envValue, 0, len(envShown))
	for _, e := range envShown {
		v := envValue{Key: e.key, Label: e.label, Effect: e.effect, Secret: isSecretEnvKey(e.key)}
		value, occurrences := file.lookup("", e.key)
		if occurrences >= 1 {
			v.Set = strings.TrimSpace(value) != ""
			if !v.Secret {
				v.Value = value
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// envFileValue reads one key straight out of the file, for the keys that are not part of the listing
// above. Used for the panel's own pin, which belongs to the deployment rather than to the game
// session and therefore has no row of its own.
func envFileValue(cfg config, key string) string {
	content, err := readTextFile(envPath(cfg))
	if err != nil {
		return ""
	}
	value, occurrences := parseINI(content).lookup("", key)
	if occurrences < 1 {
		return ""
	}
	return strings.TrimSpace(value)
}

// envModified is the file's timestamp, which the page compares against the container to say whether
// the values on screen are the ones the server is actually running with.
func envModified(cfg config) (mod os.FileInfo, err error) {
	return os.Stat(envPath(cfg))
}
