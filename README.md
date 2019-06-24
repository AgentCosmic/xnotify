# xnotify
Cross platform file notification with built-in task execution and a client/server feature to overcome virtual folders
without relying on polling.

## Features
- Works on virtual folders/shared folders like those in VirtualBox, VMWare and Docker.
- Works on Windows, Linux, OSX without polling.
- Single binary, no dependency.
- Advanced task running feature to run build commands.
- HTTP client/server can be integrated into other apps/libraries.

## Installation

Download the precompiled binaries at the [release page](https://github.com/AgentCosmic/xnotify/releases).

Or if you have Go installed you can run:
```go get github.com/AgentCosmic/xnotify```

## Tutorial

```
USAGE:
   xnotify [options] [-- <command> [args...]...]

VERSION:
   0.2.0

GLOBAL OPTIONS:
   --include value, -i value  Include path to watch recursively.
   --exclude value, -e value  Exclude files from the search using Regular Expression. This only applies to files that were passed as arguments.
   --shallow                  Disable recursive file globbing. If the path is a directory, the contents will not be included.
   --listen value             Listen on address for file changes e.g. localhost:8080 or just :8080. See --client on how to send file changes.
   --base value               Use this base path instead of the working directory. This will affect where --include finds the files. If using --listen, it will replace the original base path that was used at the sender. (default: "./")
   --client value             Send file changes to the address e.g. localhost:8080 or just :8080. See --listen on how to receive events.
   --batch milliseconds       Send the events together if they occur within given milliseconds. The program will only execute given milliseconds after the last event was fired. Only valid with -- argument. (default: 0)
   --trigger                  Run the given command immediately even if there is no file change. Only valid with -- argument.
   --verbose                  Print verbose logs.
   --help, -h                 Print this help.
   --version, -v              print the version
```

### Basic Use

Watch all files under `some_dir` and `another_dir/*.js` recursively and exclude `.git` folder. Send all events to
`build.sh` using xargs.
```
./xnotify --exclude "^\.git$" --include some_dir --include another_dir/*.js | xargs -L 1 ./build.sh
# or a shorter form
./xnotify -e "^\.git$" -i some_dir -i another_dir/*.js | xargs -L 1 ./build.sh
```

Disable recursive file matching. Only watch everything under current directory only.
```
./xnotify --shallow -i *
```

Advanced file matching using external program.
```
find *.css | ./xnotify | xargs -L 1 ./build.sh
```

### Client/server

To use file notification on a virtual file system such as VirtualBox shared folder, you need to run the app on the
host machine and VM. Both must point to the same port. Ensure the firewall is not blocking.

Watch all files in the current directory on the host machine and send events to port 8090:
```
./xnotify --client ":8090" -i .
```

On the VM:
```
./xnotify --listen "0.0.0.0:8090" --base "/opt/wwww/project" | xargs -L 1 ./build.sh
```
You need to set `--base` if the working directory path is different on the host and VM. Remember to use `0.0.0.0`
because the traffic is coming from outside the system.

### Task Runner

Run multiple commands when a file changes. Kills and runs the commands again if a new event comes before the commands
finish. Use `--batch 100` to run the command only 100ms after the last event happened. This will batch multiple
events together and execute the command only once instead of restarting it for every single event. Commands will run in
order as if the `&&` operator is used. Be careful not to run commands that spawn child processes as the child processes
_might not_ terminate with the parent processes.
```
./xnotify -i . -e "\.git$" --batch 100 -- my_lint arg1 arg2 -- ./compile.sh --flag value -- ./run.sh
```
This will run the commands in the same manner as:
```
my_lint arg1 arg2 && ./compile.sh --flag value && ./run.sh
```
You can also set the `--trigger` option if you want your command to run immediately before any file changes:
```
./xnotify -i . --trigger -- run_server.sh 8080
```

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
