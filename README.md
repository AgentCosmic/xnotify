# xnotify

Cross platform file notification with built-in task execution and a client/server feature to overcome virtual folders
without relying on polling.

## Features

- Works on virtual folders/shared folders like those in VirtualBox, VMWare and Docker.
- Works on Windows, Linux, and macOS without polling.
- Single binary, no dependency.
- Advanced task running feature to run build commands.
- HTTP client/server can be integrated into other apps/libraries.

## Installation

Download the pre-compiled binaries at the [release page](https://github.com/AgentCosmic/xnotify/releases).

Or if you have Go installed you can run:

```shell
go get github.com/AgentCosmic/xnotify
```

## Testing

```shell
make tests
```

## Tutorial

```
NAME:
   xnotify - Watch files for changes.
   File changes will be printed to stdout in the format <operation_name> <file_path>.
   stdin accepts a list of files to watch.
   Use -- to execute 1 or more commands in sequence, stopping if any command exits unsuccessfully. It will kill the old tasks if a new event is triggered.

USAGE:
   xnotify [options] [-- <command> [args...]...]

VERSION:
   0.3.0

GLOBAL OPTIONS:
   --include value, -i value  Include path to watch recursively.
   --exclude value, -e value  Exclude changes from files that match the Regular Expression. This will also apply to events received in server mode.
   --shallow                  Disable recursive file globbing. If the path is a directory, the contents will not be included.
   --listen value             Listen on address for file changes e.g. localhost:8080 or just :8080. See --client on how to send file changes.
   --base value               Use this base path instead of the working directory. This changes the root directory used by --include. If using --listen, it will replace the original base path that was used at the sender. (default: "./")
   --client value             Send file changes to the address e.g. localhost:8080 or just :8080. See --listen on how to receive events.
   --batch value              Delay emitting all events until it is idle for the given time in milliseconds (also known as debouncing). The --client argument does not support batching. (default: 0)
   --terminator value         Terminator used to terminate each batch when printing to stdout. Only active when --batch option is used. (default: "\x00")
   --trigger                  Run the given command immediately even if there is no file change. Only valid with the -- argument.
   --verbose                  Print verbose logs.
   --help, -h                 Print this help.
   --version, -v              print the version
```

### Basic Use

Watch all files under `some_dir` and `another_dir/*.js` recursively and exclude `.git` folder. Send all events to
`build.sh` using xargs.

```shell
./xnotify --exclude "^\.git$" --include some_dir --include another_dir/*.js | xargs -L 1 ./build.sh
# or a shorter form
./xnotify -e "^\.git$" -i some_dir -i another_dir/*.js | xargs -L 1 ./build.sh
```

Disable recursive file matching. Only watch everything under current directory only.

```shell
./xnotify --shallow -i *
```

Advanced file matching using external program.

```shell
find *.css | ./xnotify | xargs -L 1 ./build.sh
```

### Client/server

To use file notification on a virtual file system such as VirtualBox shared folder, you need to run the app on the
host machine and VM. Both must point to the same port. Ensure the firewall is not blocking.

Watch all files in the current directory on the host machine and send events to port 8090:

```shell
./xnotify --client ":8090" -i .
```

On the VM:

```shell
./xnotify --listen "0.0.0.0:8090" --base "/home/john/project" | xargs -L 1 ./build.sh
```

You need to set `--base` if the working directory path is different on the host and VM. Remember to use `0.0.0.0`
because the traffic is coming from outside the system.

Since the client is triggered using HTTP, you can manually send a request to the client address to trigger an event.
Send a JSON request in the following format: `{"path": "path/to/file", "operation": "event name"}`. The `operation`
field is optional as it's only used for logging. Some possible use cases would be triggering a task after a script has
finished running, or setting up multiple clients for different events.

### Task Runner

Run multiple commands when a file changes. Kills and runs the commands again if a new event comes before the commands
finish. Commands will run in
the same order as if the `&&` operator is used. Be careful not to run commands that spawn child processes as the child
processes _might not_ terminate with the parent processes.

```shell
./xnotify -i . -e "\.git$" -- my_lint arg1 arg2 -- ./compile.sh --flag value -- ./run.sh
```

This will run the commands in the same manner as:

```shell
my_lint arg1 arg2 && ./compile.sh --flag value && ./run.sh
```

You can also set the `--trigger` option if you want your command to run immediately before any file changes:

```shell
./xnotify -i . --trigger -- run_server.sh 8080
```

### Batching

Sometimes multiple file events are triggered within a very short timespan. This might cause too many processes to
spawn. To solve this we can use the `--batch` argument. This will delay the events from emitting until a certain
duration has passed since the last event &mdash; also known as debouncing. For example, by using `--batch 100`, the
events will only be emitted once the last file change is 100ms old.

Each batch will be terminated with a null character by default. You can change this using `--terminator`. Each event
will still be terminated with new lines. Here are some examples with `xargs`.

The `-0` or `--null` flags allow `xargs` to recognize each batch using the null character:

```shell
./xnotify -i . --batch 1000 | xargs -0 -L 1 ./build.sh
```

Using a different terminator such as `xxx`:

```shell
./xnotify -i . --batch 1000 --terminator xxx | xargs -d xxx -L 1 ./build.sh
```

Since the events are batched, the `$1` argument will now contain a list of events. You will have to parse this text to
extract the path information if you need it.

Batching works with the task runner too. It will only restart the tasks after the last event is emitted. The
`--terminator` flag does not apply here.

## Real World Examples

[How to Get File Notification Working on Docker, VirtualBox, VMWare or Vagrant](https://daltontan.com/file-notification-docker-virtualbox-vmware-vagrant/27)

[Go: Automatically Run Tests or Reload Web Server on File Changes](https://daltontan.com/automatically-run-tests-reload-web-server-on-file-changes/26)

[How to Compile Go Code 40% Faster With RAM Disk](https://daltontan.com/how-to-compile-go-code-faster-with-ram-disk/24)

## Similar Tools

- inotify
- fswatch
- nodemon
- entr

## Related Project

Thanks to https://github.com/fsnotify/fsnotify for the cross platform notification library.
