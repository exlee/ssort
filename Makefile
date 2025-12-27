all: license
	go build -o bin
watch:
	fd -e go | entr make all
license: cue/LICENSE.cue
	cue export cue/LICENSE.cue --out text -e license -f -o LICENSE
