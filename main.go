package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"text/tabwriter"
)

const downloadedAsinsPath = "downloaded_asins.json"

// globalFlags are shared across all commands.
type globalFlags struct {
	MediaDir string
	Password string
	Profile  string
}

// cmdSpec defines a single CLI command.
type cmdSpec struct {
	Name         string
	Desc         string
	NeedsAudible bool
	// Setup registers command-specific flags on the provided FlagSet.
	// Global flags (-media-dir, -password, -profile) are automatically added.
	Setup func(fs *flag.FlagSet)
	// Run executes the command. fs contains parsed flags.
	Run func(ctx context.Context, app *App, fs *flag.FlagSet) error
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		log.Fatalf("error: %v", err)
	}
}

func run(ctx context.Context, args []string) error {
	commands := buildCommands()

	if len(args) == 0 {
		printUsage(commands)
		return fmt.Errorf("no command specified")
	}

	name := args[0]
	if name == "help" || name == "-h" || name == "--help" {
		if len(args) > 1 {
			if cmd, ok := commands[args[1]]; ok {
				printCommandHelp(cmd)
				return nil
			}
			return fmt.Errorf("unknown command %q", args[1])
		}
		printUsage(commands)
		return nil
	}

	cmd, ok := commands[name]
	if !ok {
		printUsage(commands)
		return fmt.Errorf("unknown command %q", name)
	}

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	globals := globalFlags{}
	fs.StringVar(&globals.MediaDir, "media-dir", "media", "media directory")
	fs.StringVar(&globals.Password, "password", "", "audible auth-file password")
	fs.StringVar(&globals.Profile, "profile", "", "audible-cli profile")

	if cmd.Setup != nil {
		cmd.Setup(fs)
	}

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	app := &App{
		Audible:   newAudibleCLI(globals.Profile, globals.Password),
		Converter: newFFmpegConverter(),
		FS:        newOSFS(),
		Store:     newJSONASINStore(downloadedAsinsPath),
		Prompter:  newStdinPrompter(),
		MediaDir:  globals.MediaDir,
	}

	if cmd.NeedsAudible {
		if err := app.EnsureAudibleConfigured(ctx); err != nil {
			return err
		}
	}

	return cmd.Run(ctx, app, fs)
}

func buildCommands() map[string]cmdSpec {
	return map[string]cmdSpec{
		"download": {
			Name:         "download",
			Desc:         "Export library and download new titles",
			NeedsAudible: true,
			Run: func(ctx context.Context, app *App, _ *flag.FlagSet) error {
				if err := app.Download(ctx); err != nil {
					return fmt.Errorf("download failed: %w", err)
				}
				return app.ReadySummary()
			},
		},
		"convert": {
			Name:         "convert",
			Desc:         "Convert .aax/.aaxc files to .m4b",
			NeedsAudible: true,
			Run: func(ctx context.Context, app *App, _ *flag.FlagSet) error {
				if err := app.Convert(ctx); err != nil {
					return fmt.Errorf("convert failed: %w", err)
				}
				return app.ReadySummary()
			},
		},
		"clean": {
			Name:         "clean",
			Desc:         "Remove intermediate files from the media directory",
			NeedsAudible: false,
			Run: func(ctx context.Context, app *App, _ *flag.FlagSet) error {
				return app.Clean(ctx)
			},
		},
		"status": {
			Name:         "status",
			Desc:         "Show library and media status",
			NeedsAudible: true,
			Setup: func(fs *flag.FlagSet) {
				fs.Bool("table", false, "show all books in a table")
			},
			Run: func(ctx context.Context, app *App, fs *flag.FlagSet) error {
				showTable := fs.Lookup("table").Value.String() == "true"
				return app.Status(ctx, showTable)
			},
		},
		"ready": {
			Name:         "ready",
			Desc:         "List ready-to-listen and pending books",
			NeedsAudible: false,
			Run: func(ctx context.Context, app *App, _ *flag.FlagSet) error {
				return app.ReadySummary()
			},
		},
		"all": {
			Name:         "all",
			Desc:         "Run download and convert",
			NeedsAudible: true,
			Run: func(ctx context.Context, app *App, _ *flag.FlagSet) error {
				if err := app.Download(ctx); err != nil {
					return fmt.Errorf("download failed: %w", err)
				}
				if err := app.Convert(ctx); err != nil {
					return fmt.Errorf("convert failed: %w", err)
				}
				return app.ReadySummary()
			},
		},
	}
}

func printUsage(commands map[string]cmdSpec) {
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	fmt.Fprintln(os.Stderr, "Usage: auto-audible <command> [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, name := range commandOrder(commands) {
		cmd := commands[name]
		fmt.Fprintf(w, "  %s\t%s\n", cmd.Name, cmd.Desc)
	}
	w.Flush()
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Global flags:")
	fmt.Fprintln(os.Stderr, "  -media-dir string    media directory (default \"media\")")
	fmt.Fprintln(os.Stderr, "  -password string     audible auth-file password")
	fmt.Fprintln(os.Stderr, "  -profile string      audible-cli profile")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Run 'auto-audible help <command>' for details.")
}

func printCommandHelp(cmd cmdSpec) {
	fmt.Fprintf(os.Stderr, "Usage: auto-audible %s [flags]\n\n", cmd.Name)
	fmt.Fprintf(os.Stderr, "%s\n\n", cmd.Desc)

	fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
	fs.String("media-dir", "media", "media directory")
	fs.String("password", "", "audible auth-file password")
	fs.String("profile", "", "audible-cli profile")
	if cmd.Setup != nil {
		cmd.Setup(fs)
	}
	if hasFlags(fs) {
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.SetOutput(os.Stderr)
		fs.PrintDefaults()
	}
}

func hasFlags(fs *flag.FlagSet) bool {
	has := false
	fs.VisitAll(func(*flag.Flag) { has = true })
	return has
}

func commandOrder(commands map[string]cmdSpec) []string {
	order := []string{"download", "convert", "clean", "status", "ready", "all"}
	result := make([]string, 0, len(commands))
	for _, name := range order {
		if _, ok := commands[name]; ok {
			result = append(result, name)
		}
	}
	return result
}
