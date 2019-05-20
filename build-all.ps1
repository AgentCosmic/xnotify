$platforms = @(
		("darwin", "amd64"),
		("windows", "amd64"),
		("windows", "386"),
		("linux", "amd64"),
		("linux", "386"),
		("linux", "ppc64"),
		("linux", "ppc64le"),
		("linux", "mips64"),
		("linux", "mips64le"),
		("freebsd", "amd64"),
		("netbsd", "amd64"),
		("openbsd", "amd64"),
		("dragonfly", "amd64"),
		("solaris", "amd64")
		)

foreach ($platform in $platforms) {
	$env:GOOS = $platform[0]
	$env:GOARCH = $platform[1]
	$out = "releases/xnotify-" + $platform[0] + "-" + $platform[1]
	echo $out
	go build -o $out xnotify.go
}
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
