BIN_DIR ?= $(HOME)/.local/bin

.PHONY: build install uninstall test screenshots

build:
	go build -o lgtm .

install:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/lgtm .

uninstall:
	rm -f $(BIN_DIR)/lgtm

test:
	go test ./...

# README screenshots from demo data. Needs freeze (go install github.com/charmbracelet/freeze@latest)
# and Google Chrome, which renders the emoji that freeze's own PNG output can't.
CHROME ?= /Applications/Google Chrome.app/Contents/MacOS/Google Chrome
FREEZE ?= $(shell go env GOPATH)/bin/freeze
screenshots:
	LGTM_SCREENSHOTS=$(CURDIR)/docs go test -count=1 -run TestScreenshots .
	for f in prs diff; do \
		$(FREEZE) docs/$$f.ansi -o docs/$$f.svg --window --padding 20 --margin 0 --border.radius 8 --font.size 14 --line-height 1.2 >/dev/null && \
		printf '<html><body style="margin:0"><img src="%s.svg" style="width:1299px;height:705px;display:block"></body></html>' $$f > docs/$$f.html && \
		"$(CHROME)" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=2 --window-size=1299,705 \
			--screenshot=$(CURDIR)/docs/screenshot-$$f.png file://$(CURDIR)/docs/$$f.html >/dev/null 2>&1; \
		rm -f docs/$$f.ansi docs/$$f.svg docs/$$f.html; \
	done
