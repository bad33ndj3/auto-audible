package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Book represents an Audible library item.
type Book struct {
	ASIN           string
	Title          string
	SeriesTitle    string
	SeriesSequence interface{}
}

// MediaState tracks which books are ready or need conversion.
type MediaState struct {
	Ready     []string
	NeedsAAX  []string
	NeedsAAXC []string
}

// MediaInfo tracks what file formats exist for a single book.
type MediaInfo struct {
	HasM4B  bool
	HasAAX  bool
	HasAAXC bool
}

var activationBytesPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}$`)
var whitespacePattern = regexp.MustCompile(`\s+`)

func sanitizeFileName(name string) string {
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "",
		"?", "",
		"\"", "'",
		"<", "",
		">", "",
		"|", "-",
	)
	name = replacer.Replace(name)
	name = whitespacePattern.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	return name
}

func formatPrefix(seq interface{}) string {
	if seq == nil {
		return ""
	}
	var n int
	switch v := seq.(type) {
	case float64:
		n = int(v)
	case string:
		fmt.Sscanf(v, "%d", &n)
	default:
		return ""
	}
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%02d - ", n)
}

func extractActivationBytes(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if activationBytesPattern.MatchString(line) {
			return strings.ToLower(line)
		}
	}
	return ""
}

func computeBookState(tracked bool, info MediaInfo) string {
	if info.HasM4B {
		return "ready"
	}
	if info.HasAAX {
		return "needs_convert_aax"
	}
	if info.HasAAXC {
		return "needs_convert_aaxc"
	}
	if tracked {
		return "tracked_no_media"
	}
	return "not_downloaded"
}

func mergeMediaInfo(a, b MediaInfo) MediaInfo {
	return MediaInfo{
		HasM4B:  a.HasM4B || b.HasM4B,
		HasAAX:  a.HasAAX || b.HasAAX,
		HasAAXC: a.HasAAXC || b.HasAAXC,
	}
}

func trimCodecSuffix(base, codec string) string {
	codec = strings.TrimSpace(codec)
	if codec == "" {
		return base
	}
	suffix := "-" + codec
	if strings.HasSuffix(base, suffix) {
		return strings.TrimSuffix(base, suffix)
	}
	return base
}

func printStatusTable(rows [][3]string) {
	asinWidth := len("ASIN")
	titleWidth := len("Title")
	stateWidth := len("State")
	for _, row := range rows {
		if len(row[0]) > asinWidth {
			asinWidth = len(row[0])
		}
		if len(row[1]) > titleWidth {
			titleWidth = len(row[1])
		}
		if len(row[2]) > stateWidth {
			stateWidth = len(row[2])
		}
	}

	fmt.Printf("%-*s  %-*s  %-*s\n", asinWidth, "ASIN", titleWidth, "Title", stateWidth, "State")
	for _, row := range rows {
		fmt.Printf("%-*s  %-*s  %-*s\n", asinWidth, row[0], titleWidth, row[1], stateWidth, row[2])
	}
}

func parseLibraryJSON(fs FileSystem, filename string) ([]string, error) {
	items, err := parseLibraryItemsJSON(fs, filename)
	if err != nil {
		return nil, err
	}

	asins := make([]string, 0, len(items))
	for _, item := range items {
		asins = append(asins, item.ASIN)
	}

	return asins, nil
}

func parseLibraryItemsJSON(fs FileSystem, filename string) ([]Book, error) {
	data, err := fs.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var items []Book
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(items))
	filtered := make([]Book, 0, len(items))
	for _, item := range items {
		asin := strings.TrimSpace(item.ASIN)
		if asin == "" {
			continue
		}
		if _, ok := seen[asin]; ok {
			continue
		}
		seen[asin] = struct{}{}
		item.ASIN = asin
		filtered = append(filtered, item)
	}

	return filtered, nil
}
