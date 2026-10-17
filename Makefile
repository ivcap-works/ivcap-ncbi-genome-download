# Developer tasks for ncbigenome. Needs Go 1.21 or newer (see go.mod).

GO            ?= go
TESTFLAGS     ?=
BIN_DIR       ?= bin
FETCH_BIN     ?= $(BIN_DIR)/fetch
REPORTS_BIN   ?= $(BIN_DIR)/reports

# Used by test-download (exactly one assembly accession) and test-accession (any
# number), which call the live NCBI API.
ACCESSION     ?= GCF_000001735.4
DOWNLOAD_FILE ?= $(BIN_DIR)/$(ACCESSION).zip
FETCH_FLAGS   ?= --include genome,gff3,seq-report
REPORTS_FLAGS ?=

.DEFAULT_GOAL := help
.PHONY: help all test vet build build-example test-download test-accession clean

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: build build-example ## Build the library and the example CLIs

test: ## Run all tests, offline (extra flags: make test TESTFLAGS=-v)
	$(GO) test $(TESTFLAGS) ./...

vet: ## Run go vet
	$(GO) vet ./...

build: ## Build the library package (checks that it compiles; a library has no output file)
	$(GO) build .

build-example: ## Build the example CLIs into bin/ (fetch and reports)
	$(GO) build -o $(FETCH_BIN) ./examples/fetch
	$(GO) build -o $(REPORTS_BIN) ./examples/reports

test-download: build-example ## Download one genome with the fetch CLI (live NCBI API; set ACCESSION=...)
	@mkdir -p $(BIN_DIR)
	$(FETCH_BIN) $(FETCH_FLAGS) --filename $(DOWNLOAD_FILE) $(ACCESSION)

test-accession: build-example ## Print what AssemblyReports returns as JSON via jq (live NCBI API; needs jq)
	@command -v jq >/dev/null 2>&1 || { echo "test-accession needs jq (for example: brew install jq)" >&2; exit 1; }
	@out=$$($(REPORTS_BIN) $(REPORTS_FLAGS) $(ACCESSION)) && printf '%s\n' "$$out" | jq .

clean: ## Remove build output, including the test download
	rm -rf $(BIN_DIR)
