package conf

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/codetrek/haystack/shared/running"
	"github.com/codetrek/haystack/shared/types"
	fsutils "github.com/codetrek/haystack/utils/fs"

	"gopkg.in/yaml.v3"
)

const (
	DefaultMaxFileSize         = 5 * 1024 * 1024
	DefaultIndexWorkers        = 6
	DefaultSymbolParserWorkers = 2
	DefaultPort                = 13134

	DefaultMaxResults        = 100000
	DefaultMaxResultsPerFile = 500
	DefaultMaxFiles          = 1000
	DefaultCacheSize         = 16 * 1024 * 1024 // 16MB

	DefaultClientMaxResults        = 500
	DefaultClientMaxResultsPerFile = 50
	DefaultClientMaxFileResults    = 100

	DefaultMaxSearchWildcardLength  = 24
	DefaultMaxSearchKeywordDistance = 32
)

var (
	DefaultInclude = []string{"**/*"}
	DefaultExclude = []string{
		"node_modules/", "dist/", "build/", "vendor/", "out/", "obj/", "log/", "logs/", "debug/", "release/",
		".*", "*.log", "*.log.*", "!.github/",
	}
)

type Global struct {
	DataPath   string `yaml:"data_path,omitempty"`
	Port       int    `yaml:"port,omitempty"`
	SocketPath string `yaml:"socket_path,omitempty"`
}

type Client struct {
	DefaultWorkspace string            `yaml:"default_workspace,omitempty"`
	DefaultLimit     types.SearchLimit `yaml:"default_limit,omitempty"`
}

type Search struct {
	MaxWildcardLength  int               `yaml:"max_wildcard_length,omitempty"`
	MaxKeywordDistance int               `yaml:"max_keyword_distance,omitempty"`
	Limit              types.SearchLimit `yaml:"limit,omitempty"`
}

type Server struct {
	MaxFileSize         int64         `yaml:"max_file_size,omitempty"`
	IndexWorkers        int           `yaml:"index_workers,omitempty"`
	SymbolParserWorkers int           `yaml:"symbol_parser_workers,omitempty"`
	Filters             types.Filters `yaml:"filters,omitempty"`
	Search              Search        `yaml:"search,omitempty"`
	CacheSize           int64         `yaml:"cache_size,omitempty"`

	LoggingStdout bool `yaml:"logging_stdout,omitempty"`
}

type Symbols struct {
	EnableFeature bool `yaml:"enable_feature,omitempty"`
}

type Conf struct {
	Global  Global  `yaml:"global,omitempty"`
	Client  Client  `yaml:"client,omitempty"`
	Server  Server  `yaml:"server,omitempty"`
	Symbols Symbols `yaml:"symbols,omitempty"`
	BinPath BinPath `yaml:"bin_path,omitempty"`

	ForTest struct {
		Path string `yaml:"path,omitempty"`
	} `yaml:"for_test,omitempty"`
}

type BinPath struct {
	CTags string `yaml:"ctags,omitempty"`
}

var conf = &Conf{
	Global: Global{
		Port: DefaultPort,
	},
	Client: Client{
		DefaultWorkspace: "",
		DefaultLimit: types.SearchLimit{
			MaxResults:        DefaultClientMaxResults,
			MaxResultsPerFile: DefaultClientMaxResultsPerFile,
			MaxFilesResults:   DefaultClientMaxFileResults,
		},
	},
	Server: Server{
		MaxFileSize:         DefaultMaxFileSize,
		CacheSize:           DefaultCacheSize,
		IndexWorkers:        DefaultIndexWorkers,
		SymbolParserWorkers: DefaultSymbolParserWorkers,
		Filters: types.Filters{
			Include: DefaultInclude,
			Exclude: types.Exclude{
				UseGitIgnore: false,
				Customized:   DefaultExclude,
			},
		},
		Search: Search{
			MaxWildcardLength:  DefaultMaxSearchWildcardLength,
			MaxKeywordDistance: DefaultMaxSearchKeywordDistance,
			Limit: types.SearchLimit{
				MaxResults:        DefaultMaxResults,
				MaxResultsPerFile: DefaultMaxResultsPerFile,
				MaxFilesResults:   DefaultMaxFiles,
			},
		},
	},
	Symbols: Symbols{
		EnableFeature: true,
	},

	BinPath: BinPath{
		CTags: "",
	},
}

var confFile string

func Get() *Conf {
	return conf
}

func Load() error {
	search := []string{
		filepath.Join(running.ExecutablePath(), "config.local.yaml"),
		filepath.Join(running.ExecutablePath(), "config.yaml"),
	}

	if running.IsDevVersion() {
		search = append(search, filepath.Join(running.ExecutablePath(), "config.example.yaml"))
	}

	for _, path := range search {
		if _, err := os.Stat(path); err == nil {
			confFile = path
			break
		}
	}

	if confFile == "" {
		// Create a new config file
		confFile = filepath.Join(running.ExecutablePath(), "config.yaml")
	}

	confBytes := fsutils.ReadFileWithDefault(confFile, []byte(``))
	if err := yaml.Unmarshal(confBytes, conf); err != nil {
		return err
	}

	if conf.Global.DataPath == "" {
		conf.Global.DataPath = filepath.Join(running.UserHomeDir(), ".haystack")
	}

	if err := os.Mkdir(conf.Global.DataPath, 0755); err != nil {
		if !os.IsExist(err) {
			log.Fatalf("[Conf] Failed to create home directory: %v", err)
			return err
		}
	}

	if conf.Server.IndexWorkers <= 0 || conf.Server.IndexWorkers > runtime.NumCPU() {
		conf.Server.IndexWorkers = runtime.NumCPU()
	}

	if conf.Server.SymbolParserWorkers <= 0 || conf.Server.SymbolParserWorkers > runtime.NumCPU() {
		conf.Server.SymbolParserWorkers = runtime.NumCPU()
	}

	if conf.Server.MaxFileSize <= 0 {
		conf.Server.MaxFileSize = DefaultMaxFileSize
	}

	if conf.Server.CacheSize <= 0 {
		conf.Server.CacheSize = DefaultCacheSize
	}

	if conf.Global.Port <= 0 || conf.Global.Port > 65535 {
		// If port is not set or invalid, check if socket filename is set
		if conf.Global.SocketPath != "" {
			// If domain socket is not enabled, disable TCP server
			conf.Global.Port = 0 // 0 means no TCP server
		} else {
			// If domain socket is enabled, set default port to enable TCP server
			conf.Global.Port = DefaultPort
		}
	}

	if conf.Global.SocketPath != "" {
		// If socket filename is not an absolute path, make it absolute
		if !filepath.IsAbs(conf.Global.SocketPath) {
			conf.Global.SocketPath = filepath.Join(os.TempDir(), conf.Global.SocketPath)
		}
	}

	if conf.Server.Search.Limit.MaxResults <= 0 || conf.Server.Search.Limit.MaxResults > DefaultMaxResults {
		conf.Server.Search.Limit.MaxResults = DefaultMaxResults
	}

	if conf.Server.Search.Limit.MaxResultsPerFile <= 0 ||
		conf.Server.Search.Limit.MaxResultsPerFile > DefaultMaxResultsPerFile {
		conf.Server.Search.Limit.MaxResultsPerFile = DefaultMaxResultsPerFile
	}

	if conf.Server.Search.Limit.MaxFilesResults <= 0 ||
		conf.Server.Search.Limit.MaxFilesResults > DefaultMaxFiles {
		conf.Server.Search.Limit.MaxFilesResults = DefaultMaxFiles
	}

	if conf.Server.Search.MaxWildcardLength <= 0 ||
		conf.Server.Search.MaxWildcardLength > 64 { // 64 is the maximum length of a wildcard
		conf.Server.Search.MaxWildcardLength = DefaultMaxSearchWildcardLength
	}

	if conf.Server.Search.MaxKeywordDistance <= 0 ||
		conf.Server.Search.MaxKeywordDistance > 128 { // 128 is the maximum distance of a keyword
		conf.Server.Search.MaxKeywordDistance = DefaultMaxSearchKeywordDistance
	}

	if conf.Client.DefaultLimit.MaxResults <= 0 ||
		conf.Client.DefaultLimit.MaxResults > conf.Server.Search.Limit.MaxResults {
		conf.Client.DefaultLimit.MaxResults = DefaultClientMaxResults
	}

	if conf.Client.DefaultLimit.MaxResultsPerFile <= 0 ||
		conf.Client.DefaultLimit.MaxResultsPerFile > conf.Server.Search.Limit.MaxResultsPerFile {
		conf.Client.DefaultLimit.MaxResultsPerFile = DefaultClientMaxResultsPerFile
	}

	if conf.Client.DefaultLimit.MaxFilesResults <= 0 ||
		conf.Client.DefaultLimit.MaxFilesResults > DefaultMaxFiles {
		conf.Client.DefaultLimit.MaxFilesResults = DefaultMaxFiles
	}

	return nil
}
