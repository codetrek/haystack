package main

import (
	"flag"
	"fmt"
	"log"
	_ "net/http/pprof"
	"path/filepath"

	"github.com/codetrek/haystack/server/internal/client"
	"github.com/codetrek/haystack/server/internal/conf"
	"github.com/codetrek/haystack/server/internal/server"
	"github.com/codetrek/haystack/server/internal/shared/running"
)

var version = "dev"

// Global help flags
var (
	helpShort = flag.Bool("h", false, "Show help")
	helpLong  = flag.Bool("help", false, "Show help")
)

func init() {
	flag.Usage = func() { client.PrintUsage() }
}

func main() {
	running.SetVersion(version)

	flag.Parse()

	// Show help early if requested (skip config load)
	if *helpShort || *helpLong {
		flag.Usage()
		return
	}

	if err := conf.Load(); err != nil {
		log.Fatal("[Main] Error loading config:", err)
		return
	}

	lockFile := filepath.Join(conf.Get().Global.DataPath, "server.lock")
	running.RegisterLockFile(lockFile)

	if running.IsDaemonMode() {
		fmt.Println("Running in daemon mode")
		server.Run()
		if running.IsRestart() {
			running.StartNewServer()
		}
	} else {
		fmt.Println("Running in non-daemon mode")
		client.Run()
	}
}
