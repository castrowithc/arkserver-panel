// Backup listing, download and restore. The archives themselves are made inside the server
// container by arkmanager (cron, before an update, and on stop); the panel never creates one. It
// reads them from the shared volume and, on restore, puts their contents back where the server
// expects them.
package main

import (
	"archive/tar"
	"compress/bzip2"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type backupEntry struct {
	// ID is the path relative to the backup directory, e.g.
	// 2026-07-26/main.2026-07-26_11.14.12.tar.bz2. It comes back from the browser as an opaque
	// string and is only ever accepted when it matches an entry of a freshly built listing, so a
	// path out of a request never reaches the filesystem.
	ID   string
	Name string
	// Day is the stamp directory the archive sits in, which is how arkmanager groups them.
	Day  string
	Time string
	Size string
	// modTime is what the listing sorts on, kept apart from the rendered Time above.
	modTime time.Time
	// archiveInfo is what the archive itself says, read once per file and cached.
	archiveInfo
	// WorldSizeText is the world's own size, which is the number that matters: the archive size
	// includes the profiles and both INIs, and an archive without a world is small rather than
	// empty.
	WorldSizeText string
}

// archiveInfo is what an archive says about itself. The world and its size are read out of the
// archive; the save game cannot be, because every save game shares the instance name arkmanager
// puts in the file name, so that one comes from the sidecar the server writes beside it.
type archiveInfo struct {
	// World is the file name of the world inside, empty when the archive carries none. That happens
	// for real: a backup taken between a map switch and the first save of the new map has nothing
	// to put in, because the world file appears at the first save and not at load.
	World     string
	WorldSize int64
	// Map is the map code, taken from the world file name. Empty exactly when World is.
	Map string
	// SaveGame is the save directory the backup was taken from. Empty means unknown, which is the
	// normal state of every archive written before the stamp existed. It never means "the default
	// one": that is a different statement and would be a guess.
	SaveGame string
}

func (i archiveInfo) HasWorld() bool { return i.World != "" }

// Stamped says whether the archive knows where it came from. The page needs the distinction because
// "unknown" and "the default save game" look the same otherwise, and only one of them is safe to
// act on.
func (i archiveInfo) Stamped() bool { return i.SaveGame != "" }

func backupDir(cfg config) string { return filepath.Join(cfg.dataDir, "backup") }

// isArchive matches what arkmanager writes. Compression is optional on its side, so the plain tar
// counts too.
func isArchive(name string) bool {
	return strings.HasSuffix(name, ".tar.bz2") || strings.HasSuffix(name, ".tar")
}

// listBackups walks the day-stamp directories and returns every archive, newest first. A missing
// backup directory is not an error: a deployment that has never run its backup cron simply has
// none yet.
func listBackups(cfg config) ([]backupEntry, error) {
	root := backupDir(cfg)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}

	var out []backupEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isArchive(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		e := backupEntry{
			ID:      filepath.ToSlash(rel),
			Name:    d.Name(),
			Day:     filepath.ToSlash(filepath.Dir(rel)),
			Time:    info.ModTime().Format("2006-01-02 15:04"),
			Size:    formatMB(info.Size()),
			modTime: info.ModTime(),
		}
		// A failure here is not a failure of the listing. An archive that cannot be read still
		// exists, is still worth showing, and is still worth downloading; it simply says nothing
		// about itself, which is the same state as an old unstamped one.
		e.archiveInfo, _ = cfg.archives.lookup(path, info)
		// The stamp is read fresh every time, outside the cache. It is written by a separate step
		// right after the archive, so an archive listed in the gap between the two would otherwise
		// be remembered as unstamped for as long as the panel runs. It is also a two-line file, so
		// there is nothing to save here anyway.
		e.SaveGame = readSaveGameStamp(path)
		if e.HasWorld() {
			e.WorldSizeText = formatMB(e.WorldSize)
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].modTime.After(out[j].modTime) })
	return out, nil
}

// archiveCache remembers what each archive said about itself. Reading means decompressing the head
// of a bzip2 stream, which is cheap once and pure waste on every page load, and an archive never
// changes after it is written. Size and modification time are part of the key anyway, so a file
// replaced under the same name is read again rather than remembered wrongly.
type archiveCache struct {
	mu      sync.Mutex
	entries map[string]cachedArchive
}

type cachedArchive struct {
	size    int64
	modTime time.Time
	info    archiveInfo
}

// The methods tolerate a nil receiver, so a zero-value config behaves as an empty cache that reads
// every time instead of panicking.
func (c *archiveCache) lookup(path string, info fs.FileInfo) (archiveInfo, error) {
	if c == nil {
		return readArchiveInfo(path, info.Name())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if hit, ok := c.entries[path]; ok && hit.size == info.Size() && hit.modTime.Equal(info.ModTime()) {
		return hit.info, nil
	}
	read, err := readArchiveInfo(path, info.Name())
	if err != nil {
		return archiveInfo{}, err
	}
	if c.entries == nil {
		c.entries = map[string]cachedArchive{}
	}
	c.entries[path] = cachedArchive{size: info.Size(), modTime: info.ModTime(), info: read}
	return read, nil
}

// readArchiveInfo opens an archive far enough to answer for it, and no further. arkmanager writes
// the world first, so the loop normally stops after one header and only the first block of the
// stream is ever decompressed. Reading to the end happens only for an archive that carries no
// world, and that one is a few kilobytes.
//
// The save game is deliberately not read here: it lives in a separate file with a life of its own,
// and caching it against the archive's timestamp would freeze an answer that can still change.
func readArchiveInfo(path, name string) (archiveInfo, error) {
	var info archiveInfo

	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()

	var src io.Reader = f
	if strings.HasSuffix(name, ".bz2") {
		src = bzip2.NewReader(f)
	}
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return info, nil
		}
		if err != nil {
			return info, fmt.Errorf("archiv %s lesen: %w", name, err)
		}
		base := filepath.Base(filepath.ToSlash(h.Name))
		if h.Typeflag == tar.TypeReg && strings.HasSuffix(base, ".ark") {
			info.World, info.WorldSize = base, h.Size
			info.Map = strings.TrimSuffix(base, ".ark")
			return info, nil
		}
	}
}

// readSaveGameStamp reads the sidecar the server writes next to an archive, naming the save
// directory the backup was taken from. Absent is the ordinary case for everything backed up before
// the stamp existed and is reported as unknown, never as the default.
func readSaveGameStamp(path string) string {
	b, err := os.ReadFile(path + savegameStampSuffix)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(normalizeNewlines(string(b)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "savedir" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

const savegameStampSuffix = ".savegame"

func formatMB(b int64) string {
	return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
}

// findBackup resolves what the browser sent against the current listing. Matching an id instead of
// joining it onto the backup directory is what keeps a crafted value from pointing anywhere else.
func findBackup(entries []backupEntry, id string) (backupEntry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return backupEntry{}, false
}

func (e backupEntry) path(cfg config) string {
	return filepath.Join(backupDir(cfg), filepath.FromSlash(e.ID))
}

// restoreTargetDir routes one archive member to where the server reads it. An archive holds the
// world save, the player profiles and both INIs, flat inside a single time-stamped directory, so
// the destination follows from the file name and from nothing else.
//
// saveDir is the save directory in force, not a constant: with an alternate one configured, the
// default directory is not what the server reads, and writing a world there would restore into a
// save game nobody is playing while looking like it worked.
func restoreTargetDir(cfg config, saveDir, name string) string {
	saved := savedRoot(cfg)
	if strings.EqualFold(filepath.Ext(name), ".ini") {
		return filepath.Join(saved, "Config", "LinuxServer")
	}
	return filepath.Join(saved, saveDir)
}

// restoreConflict says why an archive must not be unpacked into the save game in force, or nil when
// there is no reason. Checked before the server is stopped, so a mismatch costs a message instead of
// a shutdown, and kept apart from the handler so the two refusals can be tested without a running
// deployment behind them.
//
// activeMap may be empty when the map could not be resolved. Then the map check is skipped rather
// than guessed, because refusing every archive would be worse than the risk it guards against.
func restoreConflict(entry backupEntry, saveDir, activeMap string) error {
	if entry.HasWorld() && activeMap != "" && !strings.EqualFold(entry.World, activeMap+".ark") {
		return fmt.Errorf(
			"Diese Sicherung enthält %s, geladen ist aber %s. Zurückspielen würde eine fremde Welt in den laufenden Spielstand schreiben.",
			entry.World, activeMap)
	}
	// One level finer than the map: two save games of one map pass the check above and are still two
	// different worlds. Only a stamped archive can be checked this way. An unstamped one predates the
	// stamp and is let through, because refusing everything older than the feature would take the
	// backups of this deployment away from it.
	if entry.Stamped() && !strings.EqualFold(entry.SaveGame, saveDir) {
		return fmt.Errorf(
			"Diese Sicherung stammt aus dem Spielstand %q, geladen ist %q. Zurückspielen würde eine fremde Welt in den laufenden Spielstand schreiben.",
			entry.SaveGame, saveDir)
	}
	return nil
}

// restoreBackup unpacks one archive over the live files. It replaces only what the archive carries
// and leaves everything else in place, because the same directory also holds arkmanager's dated
// rotation copies, which no backup contains.
//
// The caller stops the server first, and hands in the save directory in force so the world lands in
// the save game that is actually being played. Unpacking underneath a running server would achieve
// nothing: it holds the world in memory and writes it out again when it shuts down.
func restoreBackup(cfg config, entry backupEntry, saveDir string) (int, error) {
	f, err := os.Open(entry.path(cfg))
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var src io.Reader = f
	if strings.HasSuffix(entry.Name, ".bz2") {
		src = bzip2.NewReader(f)
	}

	restored := 0
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return restored, fmt.Errorf("archiv %s lesen: %w", entry.Name, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		// Only the base name is used. The archive is flat inside its stamp directory, so nothing is
		// lost, and it means no member can name its way out of the target directory.
		name := filepath.Base(filepath.ToSlash(h.Name))
		if name == "." || name == ".." || name == string(filepath.Separator) {
			continue
		}
		target := filepath.Join(restoreTargetDir(cfg, saveDir, name), name)
		if err := writeFileAtomic(target, tr); err != nil {
			return restored, fmt.Errorf("%s zurückspielen: %w", name, err)
		}
		restored++
	}
	if restored == 0 {
		return 0, fmt.Errorf("archiv %s enthält keine Dateien", entry.Name)
	}
	return restored, nil
}
