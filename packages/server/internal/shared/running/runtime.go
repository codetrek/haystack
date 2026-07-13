package running

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/codetrek/haystack/server/internal/utils"
)

var (
	userHomeDir  string
	daemonMode   = flag.Bool("daemon", false, "Run in daemon mode")
	version      string
	osExecutable = os.Executable // for test injection
)

func SetVersion(ver string) {
	if len(version) > 0 {
		return
	}
	version = ver
}

func Version() string {
	return version
}

func IsDevVersion() bool {
	return version == "dev"
}

var initHomeDir sync.Once

func UserHomeDir() string {
	initHomeDir.Do(func() {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			panic("[Running] Failed to get user's home directory: " + err.Error())
		}
		userHomeDir = homeDir
	})
	return userHomeDir
}

func IsDaemonMode() bool {
	return *daemonMode
}

func ExecutableName() string {
	return filepath.Base(Executable())
}

var once sync.Once
var executable string

func Executable() string {
	once.Do(func() {
		path, err := os.Executable()
		if err != nil {
			panic("[Running] Failed to get executable path: " + err.Error())
		}
		executable = utils.NormalizePath(path)
	})
	return executable
}

func ExecutablePath() string {
	return filepath.Dir(Executable())
}

func StartNewServer() {
	// Guard against infinite fork when the test binary spawns itself.
	// Child processes inherit this env var and bail out immediately.
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		log.Println("[Running] Skipping StartNewServer (HAYSTACK_SKIP_STARTNEW is set)")
		return
	}

	executable, err := osExecutable()
	if err != nil {
		log.Printf("[Running] Failed to get executable path: %v", err)
		return
	}

	wd, err := os.Getwd()
	if err != nil {
		log.Printf("[Running] Failed to get working directory: %v", err)
		return
	}

	args := os.Args[1:]
	env := os.Environ()
	// Propagate the guard to the child so it won't re-spawn itself
	// when it is a test binary that runs all tests again.
	env = append(env, "HAYSTACK_SKIP_STARTNEW=1")

	procAttr := &os.ProcAttr{
		Dir:   wd,
		Files: []*os.File{nil, os.Stdout, os.Stderr},
		Env:   env,
	}

	if args[0] != "--daemon" {
		// starting from client, need to start server with --daemon flag and set working directory to executable directory
		args = []string{"--daemon"}
		procAttr.Dir = filepath.Dir(executable)
	}

	process, err := os.StartProcess(executable, append([]string{executable}, args...), procAttr)
	if err != nil {
		log.Printf("[Running] Failed to start new process: %v", err)
		return
	}

	log.Printf("[Running] Started new process with PID: %d", process.Pid)
}
