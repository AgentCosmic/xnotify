package main

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

var watchList []string
var base string
var clientAddress string
var programCmd string
var pipeline int

func main() {
	watchList = make([]string, 0)
	app := cli.NewApp()
	app.Name = "xnotify"
	app.Version = "0.1.0"
	app.Usage = "Watch files for changes. You can pass a list of files to watch by stdin."
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:        "base",
			Value:       "./",
			Usage:       "base path for the client",
			Destination: &base,
		},
		cli.StringFlag{
			Name:  "listen",
			Usage: "listen to the address for file changes",
		},
		cli.StringFlag{
			Name:        "client",
			Usage:       "send file changes to the address e.g. localhost:8001",
			Destination: &clientAddress,
		},
		cli.StringFlag{
			Name:  "copy",
			Usage: "copy the new file from base directory to the given directory",
		},
		cli.StringFlag{
			Name:        "program",
			Usage:       "execute the program when a file changes",
			Destination: &programCmd,
		},
		cli.IntFlag{
			Name:        "pipeline",
			Usage:       "send the events together if they occur within the time span. Only valid for program runner.",
			Destination: &pipeline,
		},
		cli.BoolFlag{
			Name:  "recursive",
			Usage: "search directories recursively",
		},
	}
	app.Action = func(c *cli.Context) error {
		var err error
		prod := producer{
			eventChannel: make(chan Event),
		}
		base, err = filepath.Abs(base)
		if err != nil {
			log.Fatal(err)
		}
		if clientAddress != "" {
			if clientAddress[0] == ':' {
				clientAddress = "localhost" + clientAddress
			}
		}
		if pipeline != 0 {
			go execLoop(prod.eventChannel)
		}
		for _, arg := range c.Args() {
			addToWatchlist(base, arg, c.Bool("recursive"))
		}
		readStdin(base)
		if len(watchList) > 0 {
			prod.watch(watchList)
		}
		// this has to come last
		if c.String("listen") != "" {
			addr := c.String("listen")
			if addr[0] == ':' {
				addr = "localhost" + addr
			}
			prod.startServer(addr)
		}

		return nil
	}
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
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

func readStdin(base string) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		panic(err)
	}
	if fi.Size() > 0 {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			watchList = append(watchList, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			log.Println(err)
		}
	} else {
		// fmt.Println("stdin is empty")
	}
}

func addToWatchlist(base string, pattern string, recursive bool) {
	paths, err := filepath.Glob(path.Join(base, pattern))
	if err != nil {
		panic(err)
	}
	for _, p := range paths {
		watchList = append(watchList, p)
		if recursive {
			fileInfo, err := os.Stat(p)
			if err != nil {
				panic(err)
			}
			if fileInfo.IsDir() {
				addToWatchlist(p, "*", true)
			}
		}
	}
}

type producer struct {
	eventChannel chan Event
}

func (p *producer) startServer(address string) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		var e Event
		err := decoder.Decode(&e)
		if err != nil {
			panic(err)
		}
		p.fileChanged(e)
	})
	log.Print("Listening on http://" + address)
	http.ListenAndServe(address, nil)
}

func (p *producer) watch(paths []string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	done := make(chan bool)
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
						log.Panic(err)
					}
					p.fileChanged(Event{
						Operation: opToString(event.Op),
						Path:      rp,
						Time:      time.Now().UnixNano() / 1000000,
					})
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	for _, p := range paths {
		err = watcher.Add(p)
		if err != nil {
			log.Fatal(err)
		}
	}
	<-done
}

//
// ----- Runners -----
//

func (p *producer) fileChanged(e Event) {
	go printRunner(e)
	if clientAddress != "" {
		go httpRunner(e)
	}
	if programCmd != "" {
		go programRunner(p.eventChannel, e)
	}
}

func printRunner(e Event) {
	fmt.Println(e.Operation + " " + e.Path)
}

func httpRunner(e Event) {
	b, err := json.Marshal(&e)
	if err != nil {
		panic(err)
	}
	url := "http://" + clientAddress
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Println(err)
	}
	if resp.Body != nil {
		resp.Body.Close()
	}
}

//
// ----- Batch program -----
//

type eventList struct {
	events    []Event
	isRunning bool
	mux       sync.Mutex
}

var process *os.Process
var initProgamRunner sync.Once
var pendingEvents eventList

func programRunner(eventChannel chan Event, e Event) {
	initProgamRunner.Do(func() {
		pendingEvents = eventList{
			events:    make([]Event, 0),
			isRunning: false,
		}
	})
	if pipeline == 0 {
		go execProgram(e)
		return
	}

	pendingEvents.mux.Lock()
	pendingEvents.events = append(pendingEvents.events, e)
	pendingEvents.mux.Unlock()
	// kill
	if process != nil {
		process.Kill()
	}
	// run later
	dur, err := time.ParseDuration(fmt.Sprint(pipeline, "ms"))
	if err != nil {
		log.Panic(err)
	}
	time.AfterFunc(dur, func() {
		eventChannel <- e
	})
}

func execProgram(events ...Event) bool {
	println("execProgram")
	args := []string{base}
	for _, e := range events {
		args = append(append(args, e.Path), e.Operation)
	}
	cmd := exec.Command(programCmd, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Println(err)
	}
	if err := cmd.Start(); err != nil {
		log.Println(err)
	}
	process = cmd.Process
	output, err := ioutil.ReadAll(stdout)
	if err != nil {
		log.Println(err)
	}
	log.Print("Output from command:\n" + string(output))
	if err := cmd.Wait(); err != nil {
		log.Println(err)
		return false
	}
	return true
}

func execLoop(eventChannel chan Event) {
	for {
		<-eventChannel
		events := pendingEvents.events
		if len(events) == 0 {
			continue
		}
		e := events[len(events)-1]
		past := time.Now().UnixNano()/1000000 - e.Time
		if past >= int64(pipeline) {
			if execProgram(events...) {
				// reset list if successful
				pendingEvents.mux.Lock()
				pendingEvents.events = make([]Event, 0)
				pendingEvents.mux.Unlock()
			} else {
				println("exec failed")
			}
		}
	}
}
