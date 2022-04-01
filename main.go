package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bingoohuang/gg/pkg/v"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// version is the git tag at the time of build and is used to denote the
// binary's current version. This value is supplied as an ldflag at compile
// time by goreleaser (see .goreleaser.yml).
const (
	name     = "goreman"
	revision = "HEAD"
)

func usage() {
	fmt.Fprint(os.Stderr, `Tasks:
  goreman check                      # Show entries in Procfile
  goreman help [TASK]                # Show this help
  goreman export [FORMAT] [LOCATION] # Export the apps to another process
                                       (upstart)
  goreman run COMMAND [PROCESS...]   # Run a command
                                       start
                                       stop
                                       stop-all
                                       restart
                                       restart-all
                                       list
                                       status
  goreman start [PROCESS]            # Start the application
  goreman version                    # Display Goreman version

Options:
`)
	flag.PrintDefaults()
	os.Exit(0)
}

// -- process information structure.
type procInfo struct {
	name       string
	cmdline    string
	cmd        *exec.Cmd
	port       uint
	setPort    bool
	colorIndex int
	env        []string

	// True if we called stopProc to kill the process, in which case an
	// *os.ExitError is not the fault of the subprocess
	stoppedBySupervisor bool

	mu      sync.Mutex
	cond    *sync.Cond
	waitErr error
}

var mu sync.Mutex

// process informations named with proc.
var procs []*procInfo

var (
	procfile    = flag.String("f", "Procfile", "filename of Procfile")
	port        = flag.Uint("p", defaultPort(), "rpc port number")
	basedir     = flag.String("basedir", "", "base directory")
	baseport    = flag.Uint("b", 5000, "base of port numbers for app")
	setPorts    = flag.Bool("set-ports", true, "False to avoid setting PORT env var for each subprocess")
	exitOnError = flag.Bool("exit-on-error", false, "Exit goreman if a subprocess quits with a nonzero return code")
	exitOnStop  = flag.Bool("exit-on-stop", true, "Exit goreman if all subprocesses stop")
	logTime     = flag.Bool("logtime", true, "show timestamp in log")

	maxProcNameLength = 0
	re                = regexp.MustCompile(`\$([a-zA-Z]+[a-zA-Z0-9_]+)`)
)

type config struct {
	Procfile    string `yaml:"procfile"`
	Port        uint   `yaml:"port"` // Port for RPC server
	BaseDir     string `yaml:"basedir"`
	BasePort    uint   `yaml:"baseport"`
	Args        []string
	ExitOnError bool `yaml:"exit_on_error"` // If true, exit the goreman process if a subprocess exits with an error.
}

func readConfig() *config {
	var cfg config

	flag.Parse()
	if flag.NArg() == 0 {
		usage()
	}

	cfg.Procfile = *procfile
	cfg.Port = *port
	cfg.BaseDir = *basedir
	cfg.BasePort = *baseport
	cfg.ExitOnError = *exitOnError
	cfg.Args = flag.Args()

	b, err := ioutil.ReadFile(".goreman")
	if err == nil {
		yaml.Unmarshal(b, &cfg)
	}
	return &cfg
}

var exportEnvRegex = regexp.MustCompile(`^export\s+`)

// read Procfile and parse it.
func readProcfile(cfg *config) (procs []*procInfo, err error) {
	content, err := ioutil.ReadFile(cfg.Procfile)
	if err != nil {
		return procs, err
	}
	mu.Lock()
	defer mu.Unlock()

	colorIndex := 0
	var env []string

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || strings.HasPrefix(line, "//") {
			continue
		}

		exportMatch := exportEnvRegex.FindString(line)
		if exportMatch != "" {
			env = append(env, strings.TrimSpace(line[len(exportMatch):]))
			continue
		}

		tokens := strings.SplitN(line, ":", 2)
		if len(tokens) != 2 || tokens[0][0] == '#' {
			continue
		}
		k, v := strings.TrimSpace(tokens[0]), strings.TrimSpace(tokens[1])
		if runtime.GOOS == "windows" {
			v = re.ReplaceAllStringFunc(v, func(s string) string { return "%" + s[1:] + "%" })
		}
		proc := &procInfo{name: k, cmdline: v, colorIndex: colorIndex, env: env}
		if *setPorts {
			proc.setPort = true
			proc.port = cfg.BasePort
			cfg.BasePort += 100
		}
		proc.cond = sync.NewCond(&proc.mu)
		procs = append(procs, proc)
		if len(k) > maxProcNameLength {
			maxProcNameLength = len(k)
		}
		colorIndex = (colorIndex + 1) % len(colors)
	}
	if len(procs) == 0 {
		return nil, errors.New("no valid entry")
	}

	procNames := make(map[string]int)
	for _, proc := range procs {
		procNames[proc.name] = procNames[proc.name] + 1
	}
	for _, proc := range procs {
		if procNames[proc.name] == 1 {
			delete(procNames, proc.name)
		}
	}
	for i := len(procNames) - 1; i >= 0; i-- {
		proc := procs[i]
		if idx, ok := procNames[proc.name]; ok {
			proc.name += fmt.Sprintf("-%d", idx)
			procNames[proc.name] = idx - 1
		}
	}

	return procs, nil
}

func defaultServer(serverPort uint) string {
	if s, ok := os.LookupEnv("GOREMAN_RPC_SERVER"); ok {
		return s
	}
	return fmt.Sprintf("127.0.0.1:%d", defaultPort())
}

func defaultAddr() string {
	if s, ok := os.LookupEnv("GOREMAN_RPC_ADDR"); ok {
		return s
	}
	return "0.0.0.0"
}

// default port
func defaultPort() uint {
	s := os.Getenv("GOREMAN_RPC_PORT")
	if s != "" {
		if i, err := strconv.Atoi(s); err == nil {
			return uint(i)
		}
	}
	return 8555
}

// command: check. show Procfile entries.
func check(cfg *config) error {
	procs, err := readProcfile(cfg)
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	keys := make([]string, len(procs))
	for i, proc := range procs {
		keys[i] = proc.name
	}
	sort.Strings(keys)
	fmt.Printf("valid procfile detected (%s)\n", strings.Join(keys, ", "))
	return nil
}

func findProc(name string) *procInfo {
	mu.Lock()
	defer mu.Unlock()

	for _, proc := range procs {
		if proc.name == name {
			return proc
		}
	}
	return nil
}

// command: start. spawn procs.
func start(ctx context.Context, sig <-chan os.Signal, cfg *config) (err error) {
	procs, err = readProcfile(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	// Cancel the RPC server when procs have returned/errored, cancel the
	// context anyway in case of early return.
	defer cancel()
	if len(cfg.Args) > 1 {
		tmp := make([]*procInfo, 0, len(cfg.Args[1:]))
		maxProcNameLength = 0
		for _, v := range cfg.Args[1:] {
			proc := findProc(v)
			if proc == nil {
				return errors.New("unknown proc: " + v)
			}
			tmp = append(tmp, proc)
			if len(v) > maxProcNameLength {
				maxProcNameLength = len(v)
			}
		}
		mu.Lock()
		procs = tmp
		mu.Unlock()
	}
	godotenv.Load()
	rpcChan := make(chan *rpcMessage, 10)
	go startServer(ctx, rpcChan, cfg.Port)
	err = startProcs(sig, rpcChan, cfg.ExitOnError)
	return err
}

func showVersion() {
	fmt.Fprintf(os.Stdout, "%s\n", v.Version())
	os.Exit(0)
}

func main() {
	var err error
	cfg := readConfig()

	if cfg.BaseDir != "" {
		err = os.Chdir(cfg.BaseDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "goreman: %s\n", err.Error())
			os.Exit(1)
		}
	}

	cmd := cfg.Args[0]
	switch cmd {
	case "check":
		err = check(cfg)
	case "help":
		usage()
	case "run":
		if len(cfg.Args) >= 2 {
			cmd, args := cfg.Args[1], cfg.Args[2:]
			err = run(cmd, args, cfg.Port)
		} else {
			usage()
		}
	case "export":
		if len(cfg.Args) == 3 {
			format, path := cfg.Args[1], cfg.Args[2]
			err = export(cfg, format, path)
		} else {
			usage()
		}
	case "start":
		c := notifyCh()
		err = start(context.Background(), c, cfg)
	case "version":
		showVersion()
	default:
		usage()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[0], err.Error())
		os.Exit(1)
	}
}
