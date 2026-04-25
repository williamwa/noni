.PHONY: build test clean install

BIN := ./bin

build:
	mkdir -p $(BIN)
	go build -o $(BIN)/noni ./cmd/noni
	go build -o $(BIN)/nonid ./cmd/nonid

test:
	go test ./...

clean:
	rm -rf $(BIN)

install: build
	install -m 0755 $(BIN)/noni  $${HOME}/.local/bin/noni
	install -m 0755 $(BIN)/nonid $${HOME}/.local/bin/nonid
