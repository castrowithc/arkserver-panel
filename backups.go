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
}

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
		out = append(out, backupEntry{
			ID:      filepath.ToSlash(rel),
			Name:    d.Name(),
			Day:     filepath.ToSlash(filepath.Dir(rel)),
			Time:    info.ModTime().Format("2006-01-02 15:04"),
			Size:    formatMB(info.Size()),
			modTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].modTime.After(out[j].modTime) })
	return out, nil
}

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

// archiveWorld is the name of the world file an archive carries, which says which map it belongs to.
// Read before anything is restored, so a mismatch is refused while the server is still up rather
// than discovered afterwards. An archive without a world (arkmanager writes one when its
// configuration names a map whose world does not exist) comes back empty.
func archiveWorld(entry backupEntry, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var src io.Reader = f
	if strings.HasSuffix(entry.Name, ".bz2") {
		src = bzip2.NewReader(f)
	}
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("archiv %s lesen: %w", entry.Name, err)
		}
		name := filepath.Base(filepath.ToSlash(h.Name))
		if h.Typeflag == tar.TypeReg && strings.HasSuffix(name, ".ark") {
			return name, nil
		}
	}
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
