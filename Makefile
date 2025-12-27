all: license
	go generate
	go build -o bin
watch:
	fd -e go | entr make all
