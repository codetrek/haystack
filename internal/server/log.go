package server

import (
	"log"
	"os"
	"path/filepath"

	"github.com/codetrek/haystack/internal/conf"

	"gopkg.in/natefinch/lumberjack.v2"
)

// activeLog is the file logger currently installed by initLog (nil when logging to
// stdout). Tracked so closeLog can release its OS file handle. This matters on
// Windows, where an open log file blocks its directory from being removed — e.g.
// t.TempDir cleanup after Run()/initLog configured file logging under a temp dir.
var activeLog *lumberjack.Logger

func initLog() {
	log.SetFlags(log.LstdFlags)

	// Release any previously-installed file logger before replacing the output.
	closeLog()

	if conf.Get().Server.LoggingStdout {
		log.SetOutput(os.Stdout)
		return
	}

	dir := filepath.Join(conf.Get().Global.DataPath, "logs")
	logFile := filepath.Join(dir, "server.log")

	lj := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    50, // megabytes
		MaxBackups: 3,
		MaxAge:     28,   //days
		Compress:   true, // disabled by default
	}
	log.SetOutput(lj)
	activeLog = lj
}

// closeLog releases the active file logger's OS handle (if any) and reverts log
// output to stderr. Safe to call when no file logger is active. Run() defers this
// so a stopped server (or a failed startup) does not leave the log file open.
func closeLog() {
	if activeLog == nil {
		return
	}
	log.SetOutput(os.Stderr)
	_ = activeLog.Close()
	activeLog = nil
}
