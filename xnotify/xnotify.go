package xnotify

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
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/alessio/shellescape.v1"
	"gopkg.in/urfave/cli.v1"
)

// Event for each file change
type Event struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Time      int64  `json:"time"`
}

type Program struct {
	ClientAddress   string
	BatchMS         int
	Base            string
	DefaultBase     string
	eventChannel    chan Event // track file change events
	excludePatterns []string
	// used for print runner
	Terminator  string // used to terminate each batch
	mu          sync.Mutex
	timer       *time.Timer // used for debouncing
	batchEvents []Event     // collect events for next batch
	// used for task runner
	Trigger        bool        //  whether to trigger tasks immediately on startup
	hasTasks       bool        // if there is any task to run
	batchSize      int32       // keep track of the last event to trigger the runner
	tasks          [][]string  // tasks to run
	process        *os.Process // task process
	processChannel chan bool   // track the process we are spawning
}

const NullChar = "\000"

func (prog *Program) Action(c *cli.Context) (err error) {
	if c.Bool("help") {
		err = cli.ShowAppHelp(c)
		if err != nil {
			return
		}
		return
	}

	// init channels
	prog.eventChannel = make(chan Event)
	prog.processChannel = make(chan bool, 3)

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
	prog.hasTasks = len(prog.tasks) > 0

	// check for unused flags
	if prog.Base != prog.DefaultBase && c.String("listen") == "" {
		noEffect("base")
	}
	if prog.Terminator != NullChar && prog.BatchMS == 0 {
		noEffect("terminator")
	}
	if prog.Trigger && !prog.hasTasks {
		noEffect("trigger")
	}

	// convert base to absolute path
	prog.Base, err = filepath.Abs(prog.Base)
	if err != nil {
		return
	}

	// convert clientAddress to full url
	if prog.ClientAddress != "" {
		prog.ClientAddress = fullURL(prog.ClientAddress)
		logVerbose("Sending events to client at " + prog.ClientAddress)
	}

	// find files from stdin and --include
	prog.excludePatterns = c.StringSlice("exclude")
	watchList := pathsFromStdin()
	for _, arg := range c.StringSlice("include") {
		watchList = append(watchList, prog.findPaths(prog.Base, arg, !c.Bool("shallow"))...)
	}
	if Verbose {
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

	// start exec loop
	go prog.execLoop()
	if prog.hasTasks && prog.Trigger {
		prog.eventChannel <- Event{}
	}
	// watch files at paths
	if len(watchList) > 0 {
		prog.startWatching(prog.Base, watchList)
	}
	// watch files form http
	if serverAddr != "" {
		prog.startServer(fullAddress(serverAddr))
	}

	// so we don't exit
	wait := make(chan bool)
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

func (prog *Program) findPaths(base string, pattern string, recursive bool) []string {
	paths, err := filepath.Glob(path.Join(base, pattern))
	if err != nil {
		panic(err)
	}
	allPaths := make([]string, 0)
	for _, p := range paths {
		// check excludes
		if prog.isExcluded(p) {
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
				allPaths = append(allPaths, prog.findPaths(p, "*", true)...)
			}
		}
	}
	return allPaths
}

func (prog *Program) isExcluded(path string) bool {
	var rel string
	if filepath.IsAbs(path) {
		var err error
		rel, err = filepath.Rel(prog.Base, path)
		if err != nil {
			panic(err)
		}
	} else {
		rel = path
	}
	for _, ex := range prog.excludePatterns {
		excluded, err := regexp.MatchString(ex, rel)
		if err != nil {
			panic(err)
		}
		if excluded {
			return true
		}
	}
	return false
}

//
// ----- Watchers -----
//

// start server that watch for files changes from other watchers
func (prog *Program) startServer(address string) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		var e Event
		err := decoder.Decode(&e)
		if err != nil {
			logError(err)
		}
		// need to udpate time because of the time difference
		e.Time = time.Now().UnixNano() / 1000000
		prog.fileChanged(e)
	})
	logVerbose("Listening on " + address)
	go (func() {
		if err := http.ListenAndServe(address, nil); err != nil {
			panic(err)
		}
	})()
}

// start watching files at the given paths
func (prog *Program) startWatching(base string, paths []string) {
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
func (prog *Program) fileChanged(e Event) {
	if prog.isExcluded(e.Path) {
		// a file change event might cause a dir change event even though it was excluded
		return
	}
	// fmt.Printf("%+v\n", e)
	go prog.printRunner(e)
	if prog.ClientAddress != "" {
		go prog.httpRunner(e)
	}
	if prog.hasTasks {
		go prog.programRunner(prog.eventChannel, e)
	}
}

// runner that sends to a another client via http
func (prog *Program) httpRunner(e Event) {
	b, err := json.Marshal(&e)
	if err != nil {
		logError(err)
		return
	}
	req, err := http.NewRequest("POST", prog.ClientAddress, bytes.NewBuffer(b))
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

// runner that prints to stdout
func (prog *Program) printRunner(e Event) {
	prog.mu.Lock()
	defer prog.mu.Unlock()
	dur, err := time.ParseDuration(fmt.Sprint(prog.BatchMS, "ms"))
	if err != nil {
		panic(err)
	}
	if prog.timer != nil {
		prog.timer.Stop()
	}
	prog.batchEvents = append(prog.batchEvents, e)
	prog.timer = time.AfterFunc(dur, func() {
		for _, e := range prog.batchEvents {
			fmt.Printf("%s %s\n", e.Operation, shellescape.Quote(e.Path))
		}
		if dur > 0 {
			fmt.Print(prog.Terminator)
		}
		prog.batchEvents = make([]Event, 0)
	})
}

//
// ----- Batch program runner -----
//

// runner that executes an external program
func (prog *Program) programRunner(eventChannel chan Event, e Event) {
	// run later
	dur, err := time.ParseDuration(fmt.Sprint(prog.BatchMS, "ms"))
	if err != nil {
		panic(err)
	}
	atomic.AddInt32(&prog.batchSize, 1)
	time.AfterFunc(dur, func() {
		// only need to execute once the last task is here, means the batchMS time has passed since last event
		if atomic.LoadInt32(&prog.batchSize) == 1 {
			// if program is already done, there will be no effect
			if prog.process != nil {
				logVerbose("Killing program")
				prog.process.Kill()
			}
			for len(prog.processChannel) > 0 {
				// need to clear anything from previous run
				<-prog.processChannel
			}
			// tell the loop there's new event
			eventChannel <- e
			// wait until the process is captured before proceeding so we can kill it later
			<-prog.processChannel
		}
		atomic.AddInt32(&prog.batchSize, -1)
	})
}

// runs forever to try execTasks only if the batch threshold has passed, otherwise wait for the next event
func (prog *Program) execLoop() {
	for {
		<-prog.eventChannel // wait for event
		prog.execTasks()
	}
}

func (prog *Program) execTasks() bool {
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

func (prog *Program) exec(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Dir = prog.Base
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
	if len(prog.processChannel) == 0 { // don't overflow the buffer
		prog.processChannel <- true
	}
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

var Verbose = false

func logVerbose(msg interface{}) {
	if Verbose {
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
