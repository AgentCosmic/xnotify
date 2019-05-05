# xnotify
Cross platform file notification with built-in client/server and task management. Could replace some automatic build
tools.

## Features
- Works on virtual folders/shared folders like those in VirtualBox and VMWare
- Works on Windows, Linux, Mac OS
- Single binary, no dependency
- Flexible task running feature
- HTTP client/server can be integrated into other apps/libraries

## Tutorial

### Basic Use

Watch all files under `some_dir` and `another_dir/*.js` recursively and exclude `.git` folder. Send all events to
`build.sh`.
```
xnotify --exclude="^\.git$" some_dir another_dir/*.js | build.sh
```

Disable recursive file matching. Only watch everything under current directory only.
```
xnotify --shallow *
```

Advanced file matching using external program.
```
find *.css | xnotify | build.sh
```

### Client/server

To use file notification on a virtual file system such as shared folder on VirtualBox, you need to run the app on the
host machine and VM. Both must point to the same port. Ensure the firewall is not blocking.

On the host machine:
```
xnotify --client=":8090" .
```

On the VM:
```
xnotify --listen="0.0.0.0:8090" --base="/opt/wwww/project" | build.sh
```
You only need to set `--base` if you're not running from the same folder. Rmember to use `0.0.0.0` becuase the traffic
is coming from outside the system.

### Task Runner

Run `build.sh` for every file change. Kills and runs the program again if a new event comes before the program
finishes. If the program is terminated before it finishes, the events will be saved and sent with the next run.
```
xnotify --program="build.sh" .
```

Arguments are passed in the following format:
```
$base_path $rel_path1 $operation1 [$rel_path2 $operation2 $rel_path3 $operation3...]
```

To prevent events from restarting the program use `--queue`. Add `--batch="100"` so multiple events will trigger only
once after 100ms of no new events.
```
xnotify --queue --batch="100" --program="build.sh" .

```

For long running processes such as a server, use `--discard` if you do not want to keep events when the program is
restarted.
```
xnotify --discard --batch="100" --program="start-server.sh" .

```

## Related Project
Thanks to https://github.com/fsnotify/fsnotify for the cross platform notification library.
