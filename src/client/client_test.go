package client

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout captures stdout output from a function call.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error: %v", err)
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestPrintUsage(t *testing.T) {
	output := captureStdout(t, func() {
		PrintUsage()
	})
	if !strings.Contains(output, "Haystack") {
		t.Error("PrintUsage should mention Haystack")
	}
	if !strings.Contains(output, "Commands:") {
		t.Error("PrintUsage should list Commands")
	}
	if !strings.Contains(output, "search") {
		t.Error("PrintUsage should mention search command")
	}
	if !strings.Contains(output, "files") {
		t.Error("PrintUsage should mention files command")
	}
	if !strings.Contains(output, "symbols") {
		t.Error("PrintUsage should mention symbols command")
	}
	if !strings.Contains(output, "server") {
		t.Error("PrintUsage should mention server command")
	}
	if !strings.Contains(output, "workspace") {
		t.Error("PrintUsage should mention workspace command")
	}
	if !strings.Contains(output, "version") {
		t.Error("PrintUsage should mention version command")
	}
	if !strings.Contains(output, "help") {
		t.Error("PrintUsage should mention help command")
	}
	if !strings.Contains(output, "Global flags:") {
		t.Error("PrintUsage should mention Global flags")
	}
	if !strings.Contains(output, "Tips:") {
		t.Error("PrintUsage should mention Tips")
	}
}

func TestProcessCommand_Version(t *testing.T) {
	output := captureStdout(t, func() {
		processCommand([]string{"version"})
	})
	// Version outputs the running version string
	if len(output) == 0 {
		t.Error("version command should produce output")
	}
}

func TestProcessCommand_Unknown(t *testing.T) {
	output := captureStdout(t, func() {
		processCommand([]string{"nonexistent"})
	})
	if !strings.Contains(output, "Unknown command: nonexistent") {
		t.Errorf("expected unknown command message, got: %s", output)
	}
}

func TestProcessCommand_HelpNoArgs(t *testing.T) {
	output := captureStdout(t, func() {
		processCommand([]string{"help"})
	})
	if !strings.Contains(output, "Haystack") {
		t.Error("help without subcommand should print usage")
	}
}

func TestProcessCommand_HelpWithSubcommand(t *testing.T) {
	// help search should delegate to search -h
	output := captureStdout(t, func() {
		processCommand([]string{"help", "search"})
	})
	if !strings.Contains(output, "search") {
		t.Error("help search should show search help")
	}
}

func TestProcessCommand_HelpWithServer(t *testing.T) {
	output := captureStdout(t, func() {
		processCommand([]string{"help", "server"})
	})
	if !strings.Contains(output, "server") {
		t.Error("help server should show server help")
	}
}

func TestProcessCommand_HelpWithWorkspace(t *testing.T) {
	output := captureStdout(t, func() {
		processCommand([]string{"help", "workspace"})
	})
	if !strings.Contains(output, "workspace") {
		t.Error("help workspace should show workspace help")
	}
}

func TestProcessCommand_HelpWithSymbols(t *testing.T) {
	output := captureStdout(t, func() {
		processCommand([]string{"help", "symbols"})
	})
	if !strings.Contains(output, "symbols") {
		t.Error("help symbols should show symbols help")
	}
}

func TestProcessCommand_HelpWithFiles(t *testing.T) {
	output := captureStdout(t, func() {
		processCommand([]string{"help", "files"})
	})
	if !strings.Contains(output, "files") {
		t.Error("help files should show files help")
	}
}
