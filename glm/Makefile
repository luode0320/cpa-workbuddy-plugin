PLUGIN := glm-provider
VERSION := $(shell cat VERSION)

.PHONY: build clean

build:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(PLUGIN)-v$(VERSION).so .

clean:
	rm -f $(PLUGIN)-v$(VERSION).so $(PLUGIN)-v$(VERSION).h
