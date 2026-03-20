.PHONY: build install clean tidy cross-build

BINARY := dmr-plugin-gitlab
INSTALL_DIR := $(HOME)/.dmr/plugins

build: tidy
	go build -o $(BINARY) .

cross-build: tidy
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $(BINARY)-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o $(BINARY)-linux-arm64 .
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o $(BINARY)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o $(BINARY)-darwin-arm64 .

tidy:
	go mod tidy

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BINARY) $(INSTALL_DIR)/

clean:
	rm -f $(BINARY)
