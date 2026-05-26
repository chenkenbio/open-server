BINARY := open-server
PKG    := .

.PHONY: build install vet test tidy clean

build:
	go build -o $(BINARY) $(PKG)

install:
	go install $(PKG)

vet:
	go vet ./...

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
