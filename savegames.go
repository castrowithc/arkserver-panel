// Save games: which map the server loads and which directory it saves into. Both values live in
// arkmanager's instance file on the shared volume, and that file is the one place where a switch is
// possible without recreating the container: the panel writes it, then stops and starts the
// container, and the entrypoint re-reads it on the way up.
//
// Three properties of the deployment shape everything here, all three measured on a running server:
//
//   - The instance file is copied to the volume once and never overwritten afterwards, so a value
//     written into it stands. Where it still carries a ${VAR} reference, the value comes from the
//     container's environment instead, frozen in when the container was made.
//   - arkmanager re-reads the file on every invocation, and its backup takes the world named there
//     rather than the world that is running. Writing the map while the server is up therefore makes
//     the next backup archive the wrong world, or none, without failing. Hence: stop, write, start.
//   - An RCON restart does not help. It keeps the supervisor alive, and the supervisor holds the
//     configuration it started with.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// savedArksDir is arkmanager's default save directory. An alternate one sits next to it under the
// same Saved/ root, which is what makes a second, independent save game on the same map possible.
const savedArksDir = "SavedArks"

// maxSaveDirLen keeps a new directory name to something a filesystem and a human can both handle.
const maxSaveDirLen = 32

func savedRoot(cfg config) string {
	return filepath.Join(cfg.dataDir, "server", "ShooterGame", "Saved")
}

func instanceCfgPath(cfg config) string {
	return filepath.Join(cfg.dataDir, "arkmanager", "instances", "main.cfg")
}

func contentDir(cfg config) string {
	return filepath.Join(cfg.dataDir, "server", "ShooterGame", "Content")
}

// safeSaveDir accepts the names a save directory may carry. Deliberately narrow: the value is
// written into a shell-sourced configuration file and then used as a path, so anything beyond these
// characters is refused rather than escaped.
var safeSaveDir = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// reservedSaveDirs are the siblings of the save directory under Saved/. Handing one of them to the
// server as a save directory would mix save games into a directory that already means something.
var reservedSaveDirs = map[string]bool{"config": true, "logs": true}

func validSaveDir(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("kein Verzeichnis angegeben")
	case len(name) > maxSaveDirLen:
		return fmt.Errorf("Verzeichnisname länger als %d Zeichen", maxSaveDirLen)
	case !safeSaveDir.MatchString(name):
		return fmt.Errorf("Verzeichnisname darf nur Buchstaben, Zahlen, Punkt, Bindestrich und Unterstrich enthalten")
	case reservedSaveDirs[strings.ToLower(name)]:
		return fmt.Errorf("%q ist für den Server reserviert", name)
	}
	return nil
}

// mapChoice is a stock map of this edition. Code is what the server expects, dir is the directory
// whose presence proves the map is installed. The two differ often enough (ScorchedEarth_P against
// ScorchedEarth, Gen2 against Genesis2, Fjordur against FjordurOfficial) that deriving one from the
// other would be a guess, so both are written out. Six of the twelve ship as official mod maps and
// therefore live under Mods rather than Maps.
type mapChoice struct {
	Code      string
	Label     string
	dir       string
	Installed bool
}

var knownMaps = []mapChoice{
	{Code: "TheIsland", Label: "The Island", dir: "Maps/TheIslandSubMaps"},
	{Code: "TheCenter", Label: "The Center", dir: "Mods/TheCenter"},
	{Code: "ScorchedEarth_P", Label: "Scorched Earth", dir: "Maps/ScorchedEarth"},
	{Code: "Ragnarok", Label: "Ragnarok", dir: "Mods/Ragnarok"},
	{Code: "Aberration_P", Label: "Aberration", dir: "Maps/Aberration"},
	{Code: "Extinction", Label: "Extinction", dir: "Maps/Extinction"},
	{Code: "Valguero_P", Label: "Valguero", dir: "Mods/Valguero"},
	{Code: "CrystalIsles", Label: "Crystal Isles", dir: "Mods/CrystalIsles"},
	{Code: "Genesis", Label: "Genesis: Part 1", dir: "Maps/Genesis"},
	{Code: "Gen2", Label: "Genesis: Part 2", dir: "Maps/Genesis2"},
	{Code: "LostIsland", Label: "Lost Island", dir: "Mods/LostIsland"},
	{Code: "Fjordur", Label: "Fjordur", dir: "Mods/FjordurOfficial"},
}

// mapChoices marks which of the maps are on disk. A stock map that is installed needs no download,
// so switching to it costs only the world load; one that is missing would send the server into a
// download of tens of gigabytes, which is worth knowing before clicking.
func mapChoices(cfg config) []mapChoice {
	out := make([]mapChoice, len(knownMaps))
	copy(out, knownMaps)
	for i, m := range out {
		if _, err := os.Stat(filepath.Join(contentDir(cfg), filepath.FromSlash(m.dir))); err == nil {
			out[i].Installed = true
		}
	}
	return out
}

func knownMap(code string) (mapChoice, bool) {
	for _, m := range knownMaps {
		if strings.EqualFold(m.Code, code) {
			return m, true
		}
	}
	return mapChoice{}, false
}

// instanceFile is arkmanager's instance configuration, kept as its lines. Writing goes through the
// same discipline as the INI editor: one value on one line changes, every comment and every other
// line stays exactly as it was.
type instanceFile struct {
	lines []string
	// mapValue is the right-hand side of serverMap as it stands in the file, which may be a literal
	// map or a ${VAR} reference.
	mapValue string
	// saveDir is the alternate save directory, empty when the line is absent or commented out.
	saveDir  string
	mapLine  int
	saveLine int
	// saveActive says whether the save-directory line is in force or commented out, which decides
	// whether clearing it has anything to do.
	saveActive bool
}

const (
	keyServerMap = "serverMap"
	keySaveDir   = "ark_AltSaveDirectoryName"
)

func readInstanceFile(cfg config) (instanceFile, error) {
	content, err := readTextFile(instanceCfgPath(cfg))
	if err != nil {
		return instanceFile{}, err
	}
	return parseInstanceFile(content), nil
}

func parseInstanceFile(content string) instanceFile {
	f := instanceFile{lines: strings.Split(normalizeNewlines(content), "\n"), mapLine: -1, saveLine: -1}
	for i, line := range f.lines {
		body := strings.TrimSpace(line)
		commented := strings.HasPrefix(body, "#")
		body = strings.TrimLeft(body, "#")
		key, value, ok := strings.Cut(body, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), instanceValue(value)
		switch key {
		case keyServerMap:
			// A commented-out serverMap is not what the server reads, so only an active line counts.
			if !commented && f.mapLine < 0 {
				f.mapLine, f.mapValue = i, value
			}
		case keySaveDir:
			// Remember a commented line too: the template ships one, and uncommenting it in place
			// keeps the file's shape instead of appending a duplicate key further down.
			if f.saveLine < 0 || (!commented && !f.saveActive) {
				f.saveLine, f.saveActive = i, !commented
				if commented {
					value = ""
				}
				f.saveDir = value
			}
		}
	}
	return f
}

// instanceValue takes the value off a line: everything up to a trailing comment, unquoted.
func instanceValue(rest string) string {
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	return strings.Trim(strings.TrimSpace(rest), `"'`)
}

// setLineValue rewrites one line's value and keeps its trailing comment where it stood, so a diff of
// the file shows a changed value rather than a reformatted line.
func setLineValue(line, key, value string) string {
	head := key + "=" + value
	i := strings.Index(line, "#")
	if i < 0 {
		return head
	}
	if pad := i - len(head); pad > 0 {
		return head + strings.Repeat(" ", pad) + line[i:]
	}
	return head + "  " + line[i:]
}

// setMap writes the map as a literal. From here on the value stands on its own and no longer follows
// the container's environment, which is exactly the point: the environment can only be changed by
// recreating the container.
func (f *instanceFile) setMap(code string) {
	if f.mapLine < 0 {
		f.lines = append(f.lines, keyServerMap+"="+code)
		f.mapLine = len(f.lines) - 1
		f.mapValue = code
		return
	}
	f.lines[f.mapLine] = setLineValue(f.lines[f.mapLine], keyServerMap, code)
	f.mapValue = code
}

// setSaveDir points the server at a save directory, or at its default when name is empty. Clearing
// comments the line out rather than deleting it, so the previous name stays readable in the file.
func (f *instanceFile) setSaveDir(name string) {
	switch {
	case name == "" && f.saveLine < 0:
		return
	case name == "":
		line := f.lines[f.saveLine]
		if f.saveActive {
			f.lines[f.saveLine] = "#" + strings.TrimLeft(line, "#")
		}
		f.saveDir, f.saveActive = "", false
	case f.saveLine < 0:
		f.lines = append(f.lines, keySaveDir+`="`+name+`"`)
		f.saveLine, f.saveDir, f.saveActive = len(f.lines)-1, name, true
	default:
		line := strings.TrimLeft(f.lines[f.saveLine], "#")
		f.lines[f.saveLine] = setLineValue(line, keySaveDir, `"`+name+`"`)
		f.saveDir, f.saveActive = name, true
	}
}

func (f instanceFile) render() string { return strings.Join(f.lines, "\n") }

// saveDirOrDefault is the directory the server reads and writes, which is the default one unless an
// alternate is in force.
func (f instanceFile) saveDirOrDefault() string {
	if f.saveActive && f.saveDir != "" {
		return f.saveDir
	}
	return savedArksDir
}

// mapOrigin says which map applies and where that answer comes from. The distinction matters: once
// the panel has written a literal, the .env still shows the old value and is no longer what the
// server loads, and a page that hid this would make the two look like one.
type mapOrigin struct {
	Value  string
	Source string
	// Exact is false when the answer had to be taken from the file on the host instead of from the
	// running container, because the file can have been edited since the container was made.
	Exact bool
}

// resolveMap turns what stands in the instance file into the map that is actually loaded. A literal
// is the answer. A ${VAR} reference resolves from the container's own environment, which is the only
// exact source, and falls back to the .env on the host when Docker access is missing.
func resolveMap(cfg config, f instanceFile) mapOrigin {
	value := f.mapValue
	name, ok := envReference(value)
	if !ok {
		return mapOrigin{Value: value, Source: "Instanzdatei auf dem Volume", Exact: true}
	}
	if cfg.docker.configured() {
		if st, err := cfg.docker.state(); err == nil {
			if v, found := envLookup(st.Config.Env, name); found {
				return mapOrigin{Value: v, Source: "Umgebung des laufenden Containers, gesetzt aus " + name, Exact: true}
			}
		}
	}
	if values, err := loadEnvValues(cfg); err == nil {
		for _, v := range values {
			if v.Key == name {
				return mapOrigin{Value: v.Value, Source: name + " in der .env auf dem Host", Exact: false}
			}
		}
	}
	return mapOrigin{Value: "", Source: "unbekannt, " + value + " ließ sich nicht auflösen", Exact: false}
}

// envReference recognises ${VAR} and $VAR, the two forms a shell-sourced file can carry.
func envReference(value string) (string, bool) {
	if !strings.HasPrefix(value, "$") {
		return "", false
	}
	name := strings.TrimPrefix(value, "$")
	name = strings.TrimSuffix(strings.TrimPrefix(name, "{"), "}")
	if name == "" {
		return "", false
	}
	return name, true
}

func envLookup(env []string, key string) (string, bool) {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// saveGame is one world on disk: a map inside a save directory. The pair is the identity, because the
// same map can exist once per save directory and those are separate save games with separate
// characters.
type saveGame struct {
	Dir      string
	DirLabel string
	Map      string
	MapLabel string
	Size     string
	Time     string
	Active   bool
	// Fresh marks the entry the server is set to but which has no world file yet, because ARK writes
	// one at the first save and not when it loads.
	Fresh   bool
	modTime time.Time
}

func (s saveGame) ID() string { return s.Dir + "/" + s.Map }

// rotationCopy matches arkmanager's dated rotation copies, e.g. TheIsland_01.07.2026_10.45.41.ark.
// They sit in the same flat directory as the world itself and are not save games of their own.
var rotationCopy = regexp.MustCompile(`_\d{2}\.\d{2}\.\d{4}_\d{2}\.\d{2}\.\d{2}\.ark$`)

func dirLabel(dir string) string {
	if dir == savedArksDir {
		return "Standard"
	}
	return dir
}

func mapLabel(code string) string {
	if m, ok := knownMap(code); ok {
		return m.Label
	}
	return code
}

// listSaveGames walks the save directories under Saved/ and returns every world it finds, the active
// one first and the rest newest first. The active save game is always listed, even when it has no
// world file yet, so a freshly created one does not look like it failed.
func listSaveGames(cfg config, activeDir, activeMap string) ([]saveGame, error) {
	root := savedRoot(cfg)
	dirs, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	var out []saveGame
	seen := map[string]bool{}
	for _, d := range dirs {
		if !d.IsDir() || reservedSaveDirs[strings.ToLower(d.Name())] {
			continue
		}
		worlds, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		for _, w := range worlds {
			name := w.Name()
			if w.IsDir() || !strings.HasSuffix(name, ".ark") || rotationCopy.MatchString(name) {
				continue
			}
			info, err := w.Info()
			if err != nil {
				continue
			}
			code := strings.TrimSuffix(name, ".ark")
			s := saveGame{
				Dir:      d.Name(),
				DirLabel: dirLabel(d.Name()),
				Map:      code,
				MapLabel: mapLabel(code),
				Size:     formatMB(info.Size()),
				Time:     info.ModTime().Format("2006-01-02 15:04"),
				Active:   d.Name() == activeDir && strings.EqualFold(code, activeMap),
				modTime:  info.ModTime(),
			}
			seen[s.ID()] = true
			out = append(out, s)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].modTime.After(out[j].modTime)
	})

	if activeMap != "" && !seen[activeDir+"/"+activeMap] {
		out = append([]saveGame{{
			Dir:      activeDir,
			DirLabel: dirLabel(activeDir),
			Map:      activeMap,
			MapLabel: mapLabel(activeMap),
			Active:   true,
			Fresh:    true,
		}}, out...)
	}
	return out, nil
}

// applySaveGame is the whole switch: stop, write, start. The order is not a preference. Writing the
// map while the server runs leaves arkmanager's backup pointed at a world that is not loaded, and the
// shutdown backup then archives the wrong world or none at all, in both cases without failing.
func applySaveGame(cfg config, mapCode, saveDir string) error {
	if _, ok := knownMap(mapCode); !ok {
		return fmt.Errorf("unbekannte Karte %q", mapCode)
	}
	if saveDir != savedArksDir {
		if err := validSaveDir(saveDir); err != nil {
			return err
		}
	}
	// Read before stopping: a file that cannot be read is a reason not to take the server down.
	file, err := readInstanceFile(cfg)
	if err != nil {
		return fmt.Errorf("Instanzdatei nicht lesbar: %w", err)
	}

	if err := cfg.docker.stop(); err != nil {
		return fmt.Errorf("Server stoppen: %w", err)
	}

	file.setMap(mapCode)
	if saveDir == savedArksDir {
		file.setSaveDir("")
	} else {
		file.setSaveDir(saveDir)
	}
	writeErr := saveTextFile(instanceCfgPath(cfg), file.render())

	// Start again either way: leaving the deployment down after a failed write turns one problem into
	// two, and the server then simply comes back on the save game it had.
	if err := cfg.docker.start(); err != nil {
		return fmt.Errorf("Server startet nicht mehr: %w", err)
	}
	if writeErr != nil {
		return fmt.Errorf("Instanzdatei schreiben, der Server läuft wieder: %w", writeErr)
	}
	return nil
}
