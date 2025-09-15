package client

import (
	"flag"
	"fmt"
	"os"

	"github.com/ai-microsoft/haystack/shared/running"
)

func Run() {
	// If no args (only program name), show usage
	if len(os.Args) < 2 {
		PrintUsage()
		return
	}
	processCommand(os.Args[1:])
}

func processCommand(args []string) {
	command := args[0]

	switch command {
	case "search":
		handleSearch(args[1:])
	case "files":
		handleSearchFiles(args[1:])
	case "symbols":
		handleSymbols(args[1:])
	case "prompts":
		handlePrompts(args[1:])
	case "workspace":
		handleWorkspace(args[1:])
	case "server":
		handleServer(args[1:])
	case "version":
		fmt.Println(running.Version())
	case "help":
		if len(args) > 1 {
			processCommand(append(args[1:2], "-h"))
		} else {
			PrintUsage()
		}
	default:
		fmt.Printf("Unknown command: %s\n", command)
		PrintUsage()
	}
}

// PrintUsage prints the unified CLI usage with commands and global flags.
func PrintUsage() {
	fmt.Println("Haystack - local code search daemon & CLI")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  " + running.ExecutableName() + " [global flags] <command> [arguments]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  version         Show current version")
	fmt.Println("  search          Search for documents matching the query")
	fmt.Println("  files           Search for files matching the query")
	fmt.Println("  symbols         Search for symbols matching the query")
	fmt.Println("  prompts         Search for prompts matching the query")
	fmt.Println("  server          Server commands (start/stop/status/restart/run)")
	fmt.Println("  workspace       Workspace commands")
	fmt.Println("  help <command>  Show help for a specific command")
	fmt.Println()
	fmt.Println("Global flags:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Tips:")
	fmt.Println("  You can run: " + running.ExecutableName() + " <command> -h  (or --help) for command-specific options.")
}
