package main

// TODO shorter flags

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	eventChannel     chan Event
	pendingEvents    eventList
	process          *os.Process
	initProgamRunner sync.Once
	clientAddress    string
	programCmd       string
	batchMS          int
	base             string
	silent           bool
	queue            bool
	discard          bool
}

type eventList struct {
	events    []Event
	isRunning bool
	mux       sync.Mutex
}

// automatically handle duplicates
func (el *eventList) add(newEvent Event) {
	// find duplicate and delete it then append
	for i, e := range el.events {
		if e.Path == newEvent.Path {
			a := el.events
			copy(a[i:], a[i+1:])
			a[len(a)-1] = newEvent
			el.events = a
			return
		}
	}
	// else add
	el.events = append(el.events, newEvent)
}

func main() {
	prog := program{
		eventChannel: make(chan Event),
	}
	defaultBase := "./"
	app := cli.NewApp()
	app.Name = "xnotify"
	app.Version = "0.1.0"
	app.Usage = "Watch files for changes. You can pass a list of files into stdin to watch. File changes will be printed to stdout in the format [operation] [path]."
	app.UsageText = "xnotify [options] [files...]"
	app.HideHelp = true
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "shallow",
			Usage: "Disable recursive file globbing. If the path is a directory, the contents will not be included.",
		},
		cli.StringFlag{
			Name:  "exclude",
			Usage: "Exclude files from the search using Regular Expression. This only applies to files that were passed as arguments.",
		},
		cli.StringFlag{
			Name:  "listen",
			Usage: "Listen on address for file changes e.g. localhost:8080 or just :8080. See --client on how to send file changes.",
		},
		cli.StringFlag{
			Name:        "base",
			Value:       defaultBase,
			Usage:       "Base path if using --listen. This will replace the original base path that was used from the sender.",
			Destination: &prog.base,
		},
		cli.StringFlag{
			Name:        "client",
			Usage:       "Send file changes to the address e.g. localhost:8080 or just :8080. See --listen on how to receive events.",
			Destination: &prog.clientAddress,
		},
		cli.StringFlag{
			Name:        "program",
			Usage:       "Execute the `program` when a file changes i.e. program $base_path $file_path $operation",
			Destination: &prog.programCmd,
		},
		cli.IntFlag{
			Name:        "batch",
			Usage:       "Send the events together if they occur within `milliseconds`. The program will only execute given milliseconds after the last event was fired. Only valid with --program.",
			Destination: &prog.batchMS,
		},
		cli.BoolFlag{
			Name:        "queue",
			Usage:       "Keep the executing program alive instead of killing it when a new event comes. Only valid with --program.",
			Destination: &prog.queue,
		},
		cli.BoolFlag{
			Name:        "discard",
			Usage:       "Discard events that have been passed to the program even if the program exited unsuccessfully or was killed before it finished. Otherwise the events will be passed to the program again. Only valid with --program.",
			Destination: &prog.discard,
		},
		cli.BoolFlag{
			Name:        "silent",
			Usage:       "Don't print program output. Only valid with --program.",
			Destination: &prog.silent,
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
	app.Action = func(c *cli.Context) error {
		var err error

		if c.Bool("help") {
			err = cli.ShowAppHelp(c)
			if err != nil {
				panic(err)
			}
		}

		// check for unused flags
		if c.String("exclude") != "" && len(c.Args()) == 0 {
			noEffect("exclude")
		}
		if prog.base != defaultBase && c.String("listen") == "" {
			noEffect("base")
		}
		if prog.programCmd == "" {
			if prog.batchMS != 0 {
				noEffect("batch")
			}
			if prog.queue {
				noEffect("queue")
			}
			if prog.discard {
				noEffect("discard")
			}
			if prog.silent {
				noEffect("silent")
			}
		}

		// convert base to absolute path
		prog.base, err = filepath.Abs(prog.base)
		if err != nil {
			panic(err)
		}

		// convert clientAddress to full url
		if prog.clientAddress != "" {
			prog.clientAddress = fullURL(prog.clientAddress)
			logVerbose("Sending events to client at " + prog.clientAddress)
		}

		// enable pipelining
		if prog.programCmd != "" && prog.batchMS > 0 || prog.queue {
			go prog.execLoop()
		}

		// find files from stdin and args
		watchList := pathsFromStdin()
		for _, arg := range c.Args() {
			watchList = append(watchList, prog.findPaths(prog.base, arg, !c.Bool("shallow"), c.String("exclude"))...)
		}
		if verbose {
			for _, p := range watchList {
				logVerbose("Watching: " + p)
			}
		}

		// fail if nothing to watch
		serverAddr := c.String("listen")
		if serverAddr == "" && len(watchList) == 0 {
			log.Fatal("No files to watch")
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

		return nil
	}
	if err := app.Run(os.Args); err != nil {
		panic(err)
	}
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

func (prog *program) findPaths(base string, pattern string, recursive bool, exclude string) []string {
	paths, err := filepath.Glob(path.Join(base, pattern))
	if err != nil {
		panic(err)
	}
	allPaths := make([]string, 0)
	for _, p := range paths {
		if exclude != "" {
			// get relative to original base path
			rel, err := filepath.Rel(prog.base, p)
			if err != nil {
				panic(err)
			}
			excluded, err := regexp.MatchString(exclude, rel)
			if err != nil {
				panic(err)
			}
			if excluded {
				continue
			}
		}
		allPaths = append(allPaths, p)
		// if recursive and is dir, go deeper
		if recursive {
			fileInfo, err := os.Stat(p)
			if err != nil {
				panic(err)
			}
			if fileInfo.IsDir() {
				allPaths = append(allPaths, prog.findPaths(p, "*", true, exclude)...)
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
							Path:      rp,
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
	if prog.programCmd != "" {
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
	prog.initProgamRunner.Do(func() {
		prog.pendingEvents = eventList{
			events:    make([]Event, 0),
			isRunning: false,
		}
	})
	// no batching or queuingg
	if prog.batchMS == 0 && !prog.queue {
		go prog.execProgram(e)
		return
	}

	// batching starts here
	prog.pendingEvents.mux.Lock()
	prog.pendingEvents.add(e)
	prog.pendingEvents.mux.Unlock()
	// kill. If program is already done, there will be no effect
	if !prog.queue && prog.process != nil {
		logVerbose("Killing program")
		prog.process.Kill()
	}
	if prog.batchMS > 0 {
		// run later
		dur, err := time.ParseDuration(fmt.Sprint(prog.batchMS, "ms"))
		if err != nil {
			panic(err)
		}
		time.AfterFunc(dur, func() {
			eventChannel <- e
		})
	} else {
		eventChannel <- e
	}
}

// exec the program and pass the event info as arguments
func (prog *program) execProgram(events ...Event) bool {
	logVerbose("Executing program")
	args := []string{prog.base}
	for _, e := range events {
		args = append(append(args, e.Path), e.Operation)
	}
	cmd := exec.Command(prog.programCmd, args...)
	stdout, err := cmd.StdoutPipe()
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
	if !prog.silent {
		output, err := ioutil.ReadAll(stdout)
		if err != nil {
			logError(err)
			return false
		}
		if _, err = os.Stderr.Write(output); err != nil {
			logError(err)
		}
	}
	if err := cmd.Wait(); err != nil {
		log.Println(err)
		return false
	}
	return true
}

// runs forever to try execProgram only if the batch threshold has passed, otherwise wait for the next event
func (prog *program) execLoop() {
	for {
		<-prog.eventChannel           // wait for event
		prog.pendingEvents.mux.Lock() // !!! make sure all exit points have unlock
		events := prog.pendingEvents.events
		if len(events) == 0 {
			prog.pendingEvents.mux.Unlock()
			continue
		}
		e := events[len(events)-1]
		past := time.Now().UnixNano()/1000000 - e.Time
		// check if enough time has passed
		if past >= int64(prog.batchMS) {
			if prog.discard {
				prog.pendingEvents.events = make([]Event, 0)
			}
			prog.pendingEvents.mux.Unlock()
			if prog.execProgram(events...) {
				// clear list if successful
				prog.pendingEvents.mux.Lock()
				if prog.queue {
					prog.pendingEvents.events = prog.pendingEvents.events[len(events):] // only take the ones we processed
				} else {
					prog.pendingEvents.events = make([]Event, 0)
				}
				prog.pendingEvents.mux.Unlock()
			} else {
				// failed, or killed
			}
		} else {
			prog.pendingEvents.mux.Unlock()
		}
	}
}

//
// ----- Helpers -----
//

var verbose = false

func logVerbose(msg interface{}) {
	if verbose {
		log.Println(msg)
	}
}

func logError(msg interface{}) {
	log.Println(msg)
}

func noEffect(name string) {
	logError(fmt.Sprint("WARNING: --", name, " has not effect. See --help for more info."))
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

