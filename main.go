package main

import (
	"log"
	"os"

	xnotify "github.com/AgentCosmic/xnotify/xnotify"
	"gopkg.in/urfave/cli.v1"
)

func main() {
	log.SetPrefix("[xnotify] ")
	log.SetFlags(0)
	prog := xnotify.Program{
		DefaultBase:    "./",
	}
	app := cli.NewApp()
	app.Name = "xnotify"
	app.Version = "0.3.0"
	app.Usage = "Watch files for changes." +
		"\n   File changes will be printed to stdout in the format <operation_name> <file_path>." +
		"\n   stdin accepts a list of files to watch." +
		"\n   Use -- to execute 1 or more commands in sequence, stopping if any command exits unsuccessfully. It will kill the old tasks if a new event is triggered."
	app.UsageText = "xnotify [options] [-- <command> [args...]...]"
	app.HideHelp = true
	app.Flags = []cli.Flag{
		cli.StringSliceFlag{
			Name:  "include, i",
			Usage: "Include path to watch recursively.",
		},
		cli.StringSliceFlag{
			Name:  "exclude, e",
			Usage: "Exclude changes from files that match the Regular Expression. This will also apply to events received in server mode.",
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
			Value:       prog.DefaultBase,
			Usage:       "Use this base path instead of the working directory. This changes the root directory used by --include. If using --listen, it will replace the original base path that was used at the sender.",
			Destination: &prog.Base,
		},
		cli.StringFlag{
			Name:        "client",
			Usage:       "Send file changes to the address e.g. localhost:8080 or just :8080. See --listen on how to receive events.",
			Destination: &prog.ClientAddress,
		},
		cli.IntFlag{
			Name:        "batch",
			Usage:       "Delay emitting all events until it is idle for the given time in milliseconds (also known as debouncing). The --client argument does not support batching.",
			Destination: &prog.BatchMS,
		},
		cli.StringFlag{
			Name:        "terminator",
			Usage:       "Terminator used to terminate each batch when printing to stdout. Only active when --batch option is used.",
			Destination: &prog.Terminator,
			Value:       xnotify.NullChar,
		},
		cli.BoolFlag{
			Name:        "trigger",
			Usage:       "Run the given command immediately even if there is no file change. Only valid with the -- argument.",
			Destination: &prog.Trigger,
		},
		cli.BoolFlag{
			Name:        "verbose",
			Usage:       "Print verbose logs.",
			Destination: &xnotify.Verbose,
		},
		cli.BoolFlag{
			Name:  "help, h",
			Usage: "Print this help.",
		},
	}
	app.Action = prog.Action
	if err := app.Run(os.Args); err != nil {
		panic(err)
	}
}
