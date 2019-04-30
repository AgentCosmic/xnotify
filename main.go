package main

// TODO docs, shorter flags, warn if have unused flags

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

func (el *eventList) add(newEvent Event) {
	// find and replace new event
	for i, e := range el.events {
		if e.Path == newEvent.Path {
			el.events[i] = newEvent
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
	app := cli.NewApp()
	app.Name = "xnotify"
	app.Version = "0.1.0"
	app.Usage = "Watch files for changes. You can pass a list of files to watch by stdin."
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:        "base",
			Value:       "./",
			Usage:       "base path for the client",
			Destination: &prog.base,
		},
		cli.BoolFlag{
			Name:  "shallow",
			Usage: "disable recursive file globbing",
		},
		cli.StringFlag{
			Name:  "exclude",
			Usage: "exclude files from the search using Regular Expression. This does not apply to files form stdin",
		},
		cli.StringFlag{
			Name:  "listen",
			Usage: "listen to the address for file changes",
		},
		cli.StringFlag{
			Name:        "client",
			Usage:       "send file changes to the address e.g. localhost:8001",
			Destination: &prog.clientAddress,
		},
		cli.StringFlag{
			Name:        "program",
			Usage:       "execute the program when a file changes",
			Destination: &prog.programCmd,
		},
		cli.IntFlag{
			Name:        "batch",
			Usage:       "send the events together if they occur within the time span. Only valid for program runner.",
			Destination: &prog.batchMS,
		},
		cli.BoolFlag{
			Name:        "queue",
			Usage:       "keep the executing program alive instead of killing it when a new event comes. Only applies to batching.",
			Destination: &prog.queue,
		},
		cli.BoolFlag{
			Name:        "discard",
			Usage:       "discard events that have been passed to the program even if the program exited unsuccessfully",
			Destination: &prog.discard,
		},
		cli.BoolFlag{
			Name:        "silent",
			Usage:       "don't print program output",
			Destination: &prog.silent,
		},
		cli.BoolFlag{
			Name:        "verbose",
			Usage:       "print logs",
			Destination: &verbose,
		},
	}
	app.Action = func(c *cli.Context) error {
		var err error
		prog.base, err = filepath.Abs(prog.base)
		if err != nil {
			panic(err)
		}
		if prog.clientAddress != "" {
			if prog.clientAddress[0] == ':' {
				prog.clientAddress = "localhost" + prog.clientAddress
			}
			logVerbose("Sending events to client at " + prog.clientAddress)
		}
		// enable pipelining
		if prog.programCmd != "" && prog.batchMS > 0 {
			go prog.execLoop()
		}
		// find files from stdin and args
		watchList := pathsFromStdin()
		for _, arg := range c.Args() {
			watchList = append(watchList, findPaths(prog.base, arg, !c.Bool("shallow"), c.String("exclude"))...)
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
			addr := serverAddr
			if addr[0] == ':' {
				addr = "localhost" + addr
			}
			prog.startServer(addr, wait)
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

func findPaths(base string, pattern string, recursive bool, exclude string) []string {
	paths, err := filepath.Glob(path.Join(base, pattern))
	if err != nil {
		panic(err)
	}
	allPaths := make([]string, 0)
	for _, p := range paths {
		if exclude != "" {
			exlucded, err := regexp.MatchString(exclude, p)
			if err != nil {
				panic(err)
			}
			if exlucded {
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
				allPaths = append(allPaths, findPaths(p, "*", true, exclude)...)
			}
		}
	}
	return allPaths
}

//
// ----- Watchers -----
//

// start server that watch for files changes from other watchers
func (p *program) startServer(address string, done chan bool) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		var e Event
		err := decoder.Decode(&e)
		if err != nil {
			logError(err)
		}
		p.fileChanged(e)
	})
	logVerbose("Listening on http://" + address)
	go http.ListenAndServe(address, nil)
}

// start watching files at the given paths
func (p *program) startWatching(base string, paths []string, done chan bool) {
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
						p.fileChanged(Event{
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
func (p *program) fileChanged(e Event) {
	// fmt.Printf("%+v\n", e)
	go p.printRunner(e)
	if p.clientAddress != "" {
		go p.httpRunner(e)
	}
	if p.programCmd != "" {
		go p.programRunner(p.eventChannel, e)
	}
}

// runner that prints to stdout
func (p *program) printRunner(e Event) {
	fmt.Println(e.Operation + " " + e.Path)
}

// runner that sends to a another client via http
func (p *program) httpRunner(e Event) {
	b, err := json.Marshal(&e)
	if err != nil {
		logError(err)
		return
	}
	url := "http://" + p.clientAddress
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(b))
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
func (p *program) programRunner(eventChannel chan Event, e Event) {
	p.initProgamRunner.Do(func() {
		p.pendingEvents = eventList{
			events:    make([]Event, 0),
			isRunning: false,
		}
	})
	// no batching
	if p.batchMS == 0 {
		go p.execProgram(e)
		return
	}

	// batching starts here
	p.pendingEvents.mux.Lock()
	p.pendingEvents.add(e)
	p.pendingEvents.mux.Unlock()
	// kill. If program is already done, there will be no effect
	if !p.queue && p.process != nil {
		logVerbose("Killing program")
		p.process.Kill()
	}
	// run later
	dur, err := time.ParseDuration(fmt.Sprint(p.batchMS, "ms"))
	if err != nil {
		panic(err)
	}
	time.AfterFunc(dur, func() {
		eventChannel <- e
	})
}

// exec the program and pass the event info as arguments
func (p *program) execProgram(events ...Event) bool {
	logVerbose("Executing program")
	args := []string{p.base}
	for _, e := range events {
		args = append(append(args, e.Path), e.Operation)
	}
	cmd := exec.Command(p.programCmd, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logError(err)
		return false
	}
	if err := cmd.Start(); err != nil {
		logError(err)
		return false
	}
	p.process = cmd.Process
	// print program output to stderr
	if !p.silent {
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
func (p *program) execLoop() {
	for {
		<-p.eventChannel           // wait for event
		p.pendingEvents.mux.Lock() // !!! make sure all exit points have unlock
		events := p.pendingEvents.events
		if len(events) == 0 {
			p.pendingEvents.mux.Unlock()
			continue
		}
		e := events[len(events)-1]
		past := time.Now().UnixNano()/1000000 - e.Time
		// check if enough time has passed
		if past >= int64(p.batchMS) {
			if p.discard {
				p.pendingEvents.events = make([]Event, 0)
			}
			p.pendingEvents.mux.Unlock()
			if p.execProgram(events...) {
				// clear list if successful
				p.pendingEvents.mux.Lock()
				if p.queue {
					p.pendingEvents.events = p.pendingEvents.events[len(events):] // only take the ones we processed
				} else {
					p.pendingEvents.events = make([]Event, 0)
				}
				p.pendingEvents.mux.Unlock()
			} else {
				// failed, or killed
			}
		} else {
			p.pendingEvents.mux.Unlock()
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
