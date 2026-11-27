BIN_DIR ?= $(HOME)/.local/bin

.PHONY: build install uninstall test

build:
	go build -o lgtm .

install:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/lgtm .

uninstall:
	rm -f $(BIN_DIR)/lgtm

test:
	go test ./...
