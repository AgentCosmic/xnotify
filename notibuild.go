package main

// TODO shorter flags

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/urfave/cli.v1"
)

// Event for each file change
type Event struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Time      int64  `json:"time"`
}

type program struct {
	eventChannel  chan Event
	tasks         [][]string
	process       *os.Process
	clientAddress string
	batchMS       int
	batchSize     int32
	base          string
	defaultBase   string
}

func main() {
	log.SetPrefix("[xnotify] ")
	log.SetFlags(0)
	prog := program{
		eventChannel: make(chan Event),
		defaultBase:  "./",
	}
	app := cli.NewApp()
	app.Name = "xnotify"
	app.Version = "0.1.0"
	app.Usage = "Watch files for changes." +
		"\n   File changes will be printed to stdout in the format <operation_name> <file_path>." +
		"\n   stdin accepts a list of files to watch." +
		"\n   Use -- to execute 1 or more commands in sequence, stopping if any command exits unsuccessfully."
	app.UsageText = "xnotify [options] [-- <command> [args...]...]"
	app.HideHelp = true
	app.Flags = []cli.Flag{
		cli.StringSliceFlag{
			Name:  "include, i",
			Usage: "Include path to watch recursively. Defaults to current folder.",
		},
		cli.StringSliceFlag{
			Name:  "exclude, e",
			Usage: "Exclude files from the search using Regular Expression. This only applies to files that were passed as arguments.",
		},
		cli.BoolFlag{
			Name:  "shallow",
			Usage: "Disable recursive file globbing. If the path is a directory, the contents will not be included.",
		},
		cli.StringFlag{
			Name:  "listen",
			Usage: "Listen on address for file changes e.g. localhost:8080 or just :8080. See --client on how to send file changes.",
		},
		cli.StringFlag{
			Name:        "base",
			Value:       prog.defaultBase,
			Usage:       "Use this base path instead of the working directory. This will affect where --include finds the files. If using --listen, it will replace the original base path that was used at the sender.",
			Destination: &prog.base,
		},
		cli.StringFlag{
			Name:        "client",
			Usage:       "Send file changes to the address e.g. localhost:8080 or just :8080. See --listen on how to receive events.",
			Destination: &prog.clientAddress,
		},
		cli.IntFlag{
			Name:        "batch",
			Usage:       "Send the events together if they occur within given `milliseconds`. The program will only execute given milliseconds after the last event was fired. Only valid with -- arguments",
			Destination: &prog.batchMS,
		},
		cli.BoolFlag{
			Name:        "verbose",
			Usage:       "Print verbose logs.",
			Destination: &verbose,
		},
		cli.BoolFlag{
			Name:  "help, h",
			Usage: "Print this help.",
		},
	}
	app.Action = prog.action
	if err := app.Run(os.Args); err != nil {
		panic(err)
	}
}

func (prog *program) action(c *cli.Context) (err error) {
	if c.Bool("help") {
		err = cli.ShowAppHelp(c)
		if err != nil {
			return
		}
		return
	}

	// create tasks
	prog.tasks = make([][]string, 0)
	for i, arg := range os.Args {
		if isExecFlag(arg) {
			foundNext := false
			start := i + 1
			for j, a := range os.Args[start:] {
				if isExecFlag(a) {
					prog.tasks = append(prog.tasks, os.Args[start:start+j])
					foundNext = true
					break
				}
			}
			if !foundNext {
				prog.tasks = append(prog.tasks, os.Args[i+1:])
			}
		}
	}

	// check for unused flags
	if prog.base != prog.defaultBase && c.String("listen") == "" {
		noEffect("base")
	}
	if prog.batchMS > 0 && len(prog.tasks) > 0 {
		noEffect("batch")
	}

	// convert base to absolute path
	prog.base, err = filepath.Abs(prog.base)
	if err != nil {
		return
	}

	// convert clientAddress to full url
	if prog.clientAddress != "" {
		prog.clientAddress = fullURL(prog.clientAddress)
		logVerbose("Sending events to client at " + prog.clientAddress)
	}

	// start exec loop
	go prog.execLoop()

	// find files from stdin and --include
	watchList := pathsFromStdin()
	includes := c.StringSlice("include")
	if len(includes) == 0 {
		includes = []string{"."}
	}
	for _, arg := range includes {
		watchList = append(watchList, prog.findPaths(prog.base, arg, !c.Bool("shallow"), c.StringSlice("exclude"))...)
	}
	if verbose {
		for _, p := range watchList {
			logVerbose("Watching: " + p)
		}
	}

	// fail if nothing to watch
	serverAddr := c.String("listen")
	if serverAddr == "" && len(watchList) == 0 {
		logError("No files to watch. See --help on how to use this command.")
		return
	}

	var wait chan bool
	// watch files at paths
	if len(watchList) > 0 {
		prog.startWatching(prog.base, watchList, wait)
	}
	// watch files form http
	if serverAddr != "" {
		prog.startServer(fullAddress(serverAddr), wait)
	}
	_ = <-wait

	return
}

func isExecFlag(flag string) bool {
	return flag == "--"
}

func pathsFromStdin() []string {
	paths := make([]string, 0)
	fi, err := os.Stdin.Stat()
	if err != nil {
		panic(err)
	}
	if fi.Size() > 0 {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			paths = append(paths, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			panic(err)
		}
	}
	return paths
}

func (prog *program) findPaths(base string, pattern string, recursive bool, excludes []string) []string {
	paths, err := filepath.Glob(path.Join(base, pattern))
	if err != nil {
		panic(err)
	}
	allPaths := make([]string, 0)
	for _, p := range paths {
		// get relative to original base path
		rel, err := filepath.Rel(prog.base, p)
		if err != nil {
			panic(err)
		}
		// check excludes
		excluded := false
		for _, ex := range excludes {
			excluded, err = regexp.MatchString(ex, rel)
			if err != nil {
				panic(err)
			}
			if excluded {
				break
			}
		}
		if excluded {
			continue
		}
		allPaths = append(allPaths, p)
		// if recursive and is dir, go deeper
		if recursive {
			fileInfo, err := os.Stat(p)
			if err != nil {
				panic(err)
			}
			if fileInfo.IsDir() {
				allPaths = append(allPaths, prog.findPaths(p, "*", true, excludes)...)
			}
		}
	}
	return allPaths
}

//
// ----- Watchers -----
//

// start server that watch for files changes from other watchers
func (prog *program) startServer(address string, done chan bool) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		var e Event
		err := decoder.Decode(&e)
		if err != nil {
			logError(err)
		}
		// need to udpate time so because of the time difference
		e.Time = time.Now().UnixNano() / 1000000
		prog.fileChanged(e)
	})
	logVerbose("Listening on " + address)
	go http.ListenAndServe(address, nil)
}

// start watching files at the given paths
func (prog *program) startWatching(base string, paths []string, done chan bool) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	// defer watcher.Close()

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					rp, err := filepath.Rel(base, event.Name)
					if err != nil {
						logError(err)
					} else {
						prog.fileChanged(Event{
							Operation: opToString(event.Op),
							Path:      filepath.ToSlash(rp),
							Time:      time.Now().UnixNano() / 1000000,
						})
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				logError(err)
			}
		}
	}()

	for _, p := range paths {
		err = watcher.Add(p)
		if err != nil {
			panic(err)
		}
	}
}

//
// ----- Runners -----
//

// triggered when a file changes
func (prog *program) fileChanged(e Event) {
	// fmt.Printf("%+v\n", e)
	go prog.printRunner(e)
	if prog.clientAddress != "" {
		go prog.httpRunner(e)
	}
	if len(prog.tasks) > 0 {
		go prog.programRunner(prog.eventChannel, e)
	}
}

// runner that prints to stdout
func (prog *program) printRunner(e Event) {
	fmt.Println(e.Operation + " " + e.Path)
}

// runner that sends to a another client via http
func (prog *program) httpRunner(e Event) {
	b, err := json.Marshal(&e)
	if err != nil {
		logError(err)
		return
	}
	req, err := http.NewRequest("POST", prog.clientAddress, bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logError(err)
		return
	}
	if resp.Body != nil {
		resp.Body.Close()
	}
}

//
// ----- Batch program runner -----
//

// runner that executes an external program
func (prog *program) programRunner(eventChannel chan Event, e Event) {
	if prog.batchMS > 0 {
		// run later
		dur, err := time.ParseDuration(fmt.Sprint(prog.batchMS, "ms"))
		if err != nil {
			panic(err)
		}
		atomic.AddInt32(&prog.batchSize, 1)
		time.AfterFunc(dur, func() {
			if atomic.AddInt32(&prog.batchSize, -1) == 0 {
				// if program is already done, there will be no effect
				if prog.process != nil {
					logVerbose("Killing program")
					prog.process.Kill()
				}
				eventChannel <- e
			}
		})
	} else {
		eventChannel <- e
	}
}

// runs forever to try execTasks only if the batch threshold has passed, otherwise wait for the next event
func (prog *program) execLoop() {
	for {
		<-prog.eventChannel // wait for event
		prog.execTasks()
		// check if enough time has passed
		// past := time.Now().UnixNano()/1000000 - e.Time
		// if past >= int64(prog.batchMS) {
		// }
		// else just wait for the next event because it should only be called after batchMS has passed
	}
}

func (prog *program) execTasks() bool {
	logVerbose("Executing program")
	start := time.Now()
	var ok bool
	for _, task := range prog.tasks {
		if len(task) > 1 {
			ok = prog.exec(task[0], task[1:]...)
		} else {
			ok = prog.exec(task[0])
		}
		if !ok {
			break
		}
	}
	logVerbose(fmt.Sprintf("Completed in %s", time.Since(start)))
	return ok
}

func (prog *program) exec(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Dir = prog.base
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logError(err)
		return false
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		logError(err)
		return false
	}
	if err := cmd.Start(); err != nil {
		logError(err)
		return false
	}
	prog.process = cmd.Process
	// print program output to stderr
	go io.Copy(os.Stderr, stdout)
	go io.Copy(os.Stderr, stderr)
	if err := cmd.Wait(); err != nil {
		logError(err)
		return false
	}
	return true
}

//
// ----- Helpers -----
//

var verbose = false

func logVerbose(msg interface{}) {
	if verbose {
		log.Printf("\033[1;34m%s\033[0m", msg)
	}
}

func logError(msg interface{}) {
	log.Printf("\033[1;31m%s\033[0m", msg)
}

func noEffect(name string) {
	log.Printf("\033[1;33m%s\033[0m", fmt.Sprint("WARNING: --", name, " has not effect. See --help for more info."))
}

func opToString(op fsnotify.Op) string {
	switch op {
	case fsnotify.Create:
		return "create"
	case fsnotify.Write:
		return "write"
	case fsnotify.Remove:
		return "remove"
	case fsnotify.Rename:
		return "rename"
	case fsnotify.Chmod:
		return "chmod"
	}
	panic(errors.New("No such op"))
}

func fullAddress(addr string) string {
	if addr[0] == ':' {
		addr = "localhost" + addr
	}
	return addr
}

func fullURL(addr string) string {
	addr = fullAddress(addr)
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return addr
}
