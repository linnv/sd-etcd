CURDIR := $(shell pwd)

GO        := go
GOBUILD   := GOPATH=$(GOPATH) CGO_ENABLED=0 $(GO) build $(BUILD_FLAG)
GOTEST    := GOPATH=$(GOPATH) CGO_ENABLED=1 $(GO) test -p 3

LDFLAGS += -X "github.com/linnv/manhelp.Version=$(shell git describe --tags --dirty)"
LDFLAGS += -X "github.com/linnv/manhelp.BuildTime=$(shell date -u '+%Y-%m-%d %I:%M:%S')"
LDFLAGS += -X "github.com/linnv/manhelp.Branch=$(shell git rev-parse --abbrev-ref HEAD)"
LDFLAGS += -X "github.com/linnv/manhelp.GitHash=$(shell git rev-parse HEAD)"

all: help

help: ## Prints help for targets with comments
	@grep -E '^[a-zA-Z._-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

BUILDDIR=$(CURDIR)/bin
master:  ## build master service
	@mkdir -p $(BUILDDIR)
	$(GOBUILD) -ldflags '$(LDFLAGS)' -o $(BUILDDIR)/$@ $(CURDIR)/$@.go

node: ## build node service
	@mkdir -p $(BUILDDIR)
	$(GOBUILD) -ldflags '$(LDFLAGS)' -o $(BUILDDIR)/$@ $(CURDIR)/$@.go

clean: 
	@rm -rf $(BUILDDIR)

