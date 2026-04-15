package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseLibraryJSON(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "library.json")
	contents := `[
		{"asin":"B001"},
		{"asin":" B002 "},
		{"asin":"B001"},
		{"asin":""},
		{}
	]`

	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write temp library: %v", err)
	}

	asins, err := parseLibraryJSON(path)
	if err != nil {
		t.Fatalf("parseLibraryJSON returned error: %v", err)
	}

	want := []string{"B001", "B002"}
	if !reflect.DeepEqual(asins, want) {
		t.Fatalf("unexpected asins: got %v want %v", asins, want)
	}
}

func TestExtractActivationBytes(t *testing.T) {
	output := "Fetching activation bytes from Audible server\nSave activation bytes to file\na196c606\n"

	got := extractActivationBytes(output)
	if got != "a196c606" {
		t.Fatalf("unexpected activation bytes: got %q", got)
	}
}

func TestAudibleArgsSkipsEmptyPassword(t *testing.T) {
	cfg := config{Profile: "audible"}
	got := audibleArgs(cfg, "library", "list")
	want := []string{"--profile", "audible", "library", "list"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args: got %v want %v", got, want)
	}
}
