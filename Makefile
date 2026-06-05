.PHONY: build run

build:
	go build -o /dev/null .

run: build
	go run .
