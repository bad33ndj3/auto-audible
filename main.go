package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// downloadedAsinsPath is the local JSON file that keeps track of downloaded ASINs.
const downloadedAsinsPath = "downloaded_asins.json"

// libraryTSV is the name of the exported TSV file from "audible library export".
const libraryTSV = "library.tsv"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "download":
		authPwd, err := promptPassword("Please enter your auth-file password: ")
		if err != nil {
			log.Fatalf("failed to read password: %v", err)
		}
		if err := doDownload(authPwd); err != nil {
			log.Fatalf("download failed: %v", err)
		}
	case "decrypt":
		authPwd, err := promptPassword("Please enter your auth-file password: ")
		if err != nil {
			log.Fatalf("failed to read password: %v", err)
		}
		if err := doDecrypt(authPwd); err != nil {
			log.Fatalf("decrypt failed: %v", err)
		}
	case "clean":
		if err := doClean(); err != nil {
			log.Fatalf("clean failed: %v", err)
		}
	case "all":
		authPwd, err := promptPassword("Please enter your auth-file password: ")
		if err != nil {
			log.Fatalf("failed to read password: %v", err)
		}
		if err := doDownload(authPwd); err != nil {
			log.Fatalf("download failed: %v", err)
		}
		if err := doDecrypt(authPwd); err != nil {
			log.Fatalf("decrypt failed: %v", err)
		}
	default:
		usage()
		os.Exit(1)
	}
}

// delete all files of type aaxc, aax, jpg, json, voucher and pdf in the folder media
func doClean() error {
	files, err := os.ReadDir("media")
	if err != nil {
		return fmt.Errorf("failed to read media directory: %w", err)
	}

	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".aaxc") ||
			strings.HasSuffix(file.Name(), ".aax") ||
			strings.HasSuffix(file.Name(), ".jpg") ||
			strings.HasSuffix(file.Name(), ".json") ||
			strings.HasSuffix(file.Name(), ".voucher") ||
			strings.HasSuffix(file.Name(), ".pdf") {
			if err := os.Remove("media/" + file.Name()); err != nil {
				return fmt.Errorf("failed to remove %s: %w", file.Name(), err)
			}
		}
	}

	return nil
}

func usage() {
	fmt.Println("Usage:")
	fmt.Println("  go run main.go download")
	fmt.Println("  go run main.go decrypt")
	fmt.Println("  go run main.go all")
}

// doDownload prompts for password, exports the library, parses library.tsv, and
// downloads books whose ASINs are not yet in downloaded_asins.json.
func doDownload(authPwd string) error {
	fmt.Println("Exporting library to", libraryTSV)
	if err := runCmd("audible", "--password", authPwd, "library", "export"); err != nil {
		return fmt.Errorf("failed to export library: %w", err)
	}

	asins, err := parseLibraryTSV(libraryTSV)
	if err != nil {
		return fmt.Errorf("failed to parse %s: %w", libraryTSV, err)
	}

	downloaded, err := loadDownloadedAsins()
	if err != nil {
		return fmt.Errorf("failed to load downloaded asins: %w", err)
	}

	for _, asin := range asins {
		if asin == "" {
			continue
		}
		if !contains(downloaded, asin) {
			fmt.Printf("Downloading new book with ASIN: %s\n", asin)
			// Perform the audible download with the user’s desired flags.
			// Pass the password each time.
			err := runCmd(
				"audible",
				"--password", authPwd,
				"download",
				"--asin", asin,
				"--output-dir", "media",
				"-y",
				"--ignore-errors",
				"--aax-fallback",
				"--timeout", "5000",
				"--pdf",
				"--cover",
				"--chapter",
				"-q", "high",
				"--overwrite",
				"--ignore-podcasts",
			)
			if err != nil {
				// Don’t add the ASIN to our list if the download failed.
				fmt.Fprintf(os.Stderr, "Failed to download ASIN %s: %v\n", asin, err)
				continue
			}
			// Add to downloaded list and save.
			downloaded = append(downloaded, asin)
			if err := saveDownloadedAsins(downloaded); err != nil {
				return fmt.Errorf("failed to save downloaded asins: %w", err)
			}
		} else {
			fmt.Printf("ASIN %s already downloaded, skipping.\n", asin)
		}
	}
	return nil
}

// doDecrypt prompts for password, then decrypts all downloaded AAX files.
func doDecrypt(authPwd string) error {
	fmt.Println("Decrypting all downloaded AAX files...")
	runCmd("cd", "media")
	defer runCmd("cd", "..")
	return runCmd(
		"audible", "--password", authPwd,
		"decrypt",
		"--all",
		"--overwrite",
	)
}

// promptPassword reads a password from stdin without echoing to the terminal.
func promptPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	// Turn off input echoing, read the password, then restore.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return "", err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	line, err := readLine(os.Stdin)
	fmt.Println() // just to move to a new line after user presses Enter
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readLine is a minimal raw input reader used by promptPassword.
func readLine(r io.Reader) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' || buf[0] == '\r' {
				break
			}
			sb.WriteByte(buf[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	return sb.String(), nil
}

// parseLibraryTSV reads library.tsv (generated by `audible library export`) and returns
// a slice of ASINs. We assume the first column is "asin", and the first row is a header row.
func parseLibraryTSV(filename string) ([]string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var asins []string
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		// Skip the header (first line).
		if lineCount == 1 {
			continue
		}
		line := scanner.Text()
		// TSV means columns are separated by tabs.
		cols := strings.Split(line, "\t")
		if len(cols) < 1 {
			// Skip bad line or handle error
			continue
		}
		// The first column is the ASIN
		asin := cols[0]
		asins = append(asins, asin)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return asins, nil
}

// loadDownloadedAsins loads the JSON array from downloaded_asins.json (if present).
func loadDownloadedAsins() ([]string, error) {
	if _, err := os.Stat(downloadedAsinsPath); os.IsNotExist(err) {
		// If the file doesn't exist, return an empty slice.
		return []string{}, nil
	}

	data, err := os.ReadFile(downloadedAsinsPath)
	if err != nil {
		return nil, err
	}

	var asins []string
	if err := json.Unmarshal(data, &asins); err != nil {
		return nil, err
	}
	return asins, nil
}

// saveDownloadedAsins overwrites downloaded_asins.json with the updated list.
func saveDownloadedAsins(asins []string) error {
	data, err := json.MarshalIndent(asins, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(downloadedAsinsPath, data, 0o644)
}

// runCmd is a helper to run an external command and forward its stdout/stderr.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// contains checks if slice s contains string val.
func contains(s []string, val string) bool {
	for _, item := range s {
		if item == val {
			return true
		}
	}
	return false
}
