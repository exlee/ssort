all: license
	go generate
	go test
	go build -o bin
watch:
	fd -e go | entr make all
