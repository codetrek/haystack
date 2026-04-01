package server

import (
	"log"
	"os"
	"path/filepath"

	"github.com/codetrek/haystack/conf"

	"gopkg.in/natefinch/lumberjack.v2"
)

func initLog() {
	log.SetFlags(log.LstdFlags)

	if conf.Get().Server.LoggingStdout {
		log.SetOutput(os.Stdout)
	} else {
		dir := filepath.Join(conf.Get().Global.DataPath, "logs")
		logFile := filepath.Join(dir, "server.log")

		log.SetOutput(&lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    50, // megabytes
			MaxBackups: 3,
			MaxAge:     28,   //days
			Compress:   true, // disabled by default
		})
	}
}
