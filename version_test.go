package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Trimmed copy of a real manifest, tabs and all, including the nested block that follows the flat
// keys and the TargetBuildID that must not be mistaken for buildid.
const manifestFixture = "\"AppState\"\n{\n" +
	"\t\"appid\"\t\t\"376030\"\n" +
	"\t\"name\"\t\t\"ARK: Survival Evolved Dedicated Server\"\n" +
	"\t\"buildid\"\t\t\"21241282\"\n" +
	"\t\"TargetBuildID\"\t\t\"21241283\"\n" +
	"\t\"InstalledDepots\"\n\t{\n\t\t\"376031\"\n\t\t{\n\t\t\t\"manifest\"\t\t\"123\"\n\t\t}\n\t}\n}\n"

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := manifestPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func TestInstalledBuild(t *testing.T) {
	got, err := installedBuild(writeManifest(t, manifestFixture))
	if err != nil {
		t.Fatalf("installedBuild: %v", err)
	}
	if got != "21241282" {
		t.Errorf("want 21241282, got %q", got)
	}
}

func TestInstalledBuildMissingManifest(t *testing.T) {
	if _, err := installedBuild(t.TempDir()); err == nil {
		t.Fatal("want an error when the manifest is absent, got none")
	}
}

func TestInstalledBuildWithoutKey(t *testing.T) {
	if _, err := installedBuild(writeManifest(t, "\"AppState\"\n{\n\t\"appid\"\t\t\"376030\"\n}\n")); err == nil {
		t.Fatal("want an error when buildid is absent, got none")
	}
}

func TestACFValue(t *testing.T) {
	tests := []struct{ key, want string }{
		{"buildid", "21241282"},
		{"TargetBuildID", "21241283"},
		{"name", "ARK: Survival Evolved Dedicated Server"},
		{"absent", ""},
		{"InstalledDepots", ""}, // a block header has no value on its line
	}
	for _, tt := range tests {
		if got := acfValue(manifestFixture, tt.key); got != tt.want {
			t.Errorf("%s: want %q, got %q", tt.key, tt.want, got)
		}
	}
}
