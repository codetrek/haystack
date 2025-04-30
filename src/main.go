package main

import (
	"flag"
	"fmt"
	"log"
	_ "net/http/pprof"
	"path/filepath"

	"github.com/ai-microsoft/haystack/client"
	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server"
	"github.com/ai-microsoft/haystack/shared/running"
)

var version = "dev"

func main() {
	running.SetVersion(version)

	flag.Parse()
	if err := conf.Load(); err != nil {
		log.Fatal("Error loading config:", err)
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
