// Installed-version reporting. ARK's RCON has no version command and the game writes no version
// file anywhere on the volume, so the panel reports SteamCMD's build id from the app manifest. That
// is also the number an update compares against, which makes it the more useful of the two.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const arkAppID = "376030"

func manifestPath(dataDir string) string {
	return filepath.Join(dataDir, "server", "steamapps", "appmanifest_"+arkAppID+".acf")
}

// installedBuild reads the build id of the installed server.
func installedBuild(dataDir string) (string, error) {
	path := manifestPath(dataDir)
	text, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("app manifest: %w", err)
	}
	build := acfValue(string(text), "buildid")
	if build == "" {
		return "", fmt.Errorf("app manifest %s carries no buildid", path)
	}
	return build, nil
}

// acfValue pulls one value out of Valve's KeyValues text. A real parser would be wasted here: every
// line that matters reads `"key"<tabs>"value"`, so splitting on the quotes is enough and cannot be
// fooled by the nested blocks further down, which have no value on the same line.
func acfValue(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Split(line, `"`)
		if len(parts) >= 5 && parts[1] == key {
			return parts[3]
		}
	}
	return ""
}
