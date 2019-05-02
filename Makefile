GOFILES := $(wildcard *.go)
BINARY := imapidol

$(BINARY): $(GOFILES)
	go build -v -o $(BINARY) .

.PHONY: run
run: $(BINARY)
	./$(BINARY)

.PHONY: clean
clean:
	rm -f $(BINARY)
