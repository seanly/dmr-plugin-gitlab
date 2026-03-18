.PHONY: build install clean tidy

BINARY := dmr-plugin-gitlab
INSTALL_DIR := $(HOME)/.dmr/plugins

build: tidy
	go build -o $(BINARY) .

tidy:
	go mod tidy

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BINARY) $(INSTALL_DIR)/

clean:
	rm -f $(BINARY)
