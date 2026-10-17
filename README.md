# ncbigenome

A small Go library (standard library only; module `github.com/ivcap-works/ivcap-ncbi-genome-download`) that does what

    datasets download genome accession <ACCESSION...> [--include ...]

does against the NCBI Datasets v2 REST API, extracted from the
[`ncbi/datasets`](https://github.com/ncbi/datasets)
CLI source (release v18.39.0). No cobra, no progress bars, no generated client.

## Install

```
go get github.com/ivcap-works/ivcap-ncbi-genome-download
```

The package name is `ncbigenome`, which differs from the last element of the
import path, so import it with an explicit name:

```go
import ncbigenome "github.com/ivcap-works/ivcap-ncbi-genome-download"
```

## Use

```go
c := ncbigenome.New(ncbigenome.Options{APIKey: os.Getenv("NCBI_API_KEY")})
res, err := c.DownloadToFile(ctx, ncbigenome.Request{
    Accessions: []string{"GCF_000001735.4"},
    Include:    []ncbigenome.Include{ncbigenome.IncludeGenome, ncbigenome.IncludeGFF3, ncbigenome.IncludeSeqReport},
}, "TAIR10.1.zip")
```

`Download(ctx, req, w)` streams the zip to any `io.Writer` instead of a file.

### One genome per download

`Download` and `DownloadToFile` are restricted to a single genome, so a package
always holds one assembly's files and one set of metadata. The rule is enforced
before anything is downloaded:

- `Request.Accessions` must hold exactly one assembly accession (`GCA_` or
  `GCF_`). A BioProject accession is rejected, because a project can name many
  genomes: use `AssemblyReports` to see which assemblies it has and pass one of
  them. Repeating the same accession is fine.
- After the lookup the request must match exactly one assembly. This can fail
  with `AssemblyVersion: VersionAll`, which asks for every version of an
  assembly; the error lists the accessions found (at most five). No match gives
  `ErrNoAssemblies`.

Both rejections wrap `ErrNotSingleGenome`, so test with `errors.Is`.
`Result.Accession` is the one assembly that was downloaded. `AssemblyReports`
only looks things up, so it still takes any number of accessions.

### AssemblyReports: which assemblies would this request download?

```go
reports, err := c.AssemblyReports(ctx, ncbigenome.Request{
    Accessions: []string{"GCF_000001735"}, // no version given
})
// reports[0].Accession is "GCF_000001735.4" while that is the current version
fmt.Println(reports[0].AssemblyInfo.AssemblyName, reports[0].Organism.OrganismName)
accessions := ncbigenome.Accessions(reports) // just the accession strings
```

It answers the question without downloading anything, and returns NCBI's full
genome assembly report for each assembly (`[]AssemblyDataReport`, see
[Report types and JSON Schema](#report-types-and-json-schema)). Every download starts with the same lookup, asking only for accessions, which is
lighter; this method exposes it with the full reports. It makes the same
`POST /genome/dataset_report` calls as the CLI and uses only `Accessions`,
`AssemblyVersion` and `Filters` from the request.

What it does:

- checks and upper-cases the accessions: assembly accessions (`GCA_`/`GCF_`,
  optionally versioned) or BioProject accessions (`PRJNA`, `PRJDA`, `PRJDB`,
  `PRJEA`, `PRJEB`);
- applies the version policy: keep a version you gave (`.4`), strip versions to
  get the current one, or return all versions;
- asks NCBI which assemblies match, following the result pages. BioProject
  accessions expand to their assemblies, and any `Filters` are applied;
- returns the full report of each match, de-duplicated by accession.

What it is useful for:

- seeing what a BioProject expands to before committing to a large download;
- checking that accessions exist, and getting the concrete version together with
  the assembly's metadata (organism, assembly statistics, annotation counts,
  BioSample) in one call, ready to record next to your data;
- trying out filter combinations cheaply.

Notes: it needs network access. Invalid accessions or filters are rejected
before any request is sent. An empty result is not an error here; `Download` and
`DownloadToFile` return `ErrNoAssemblies` in that case. A full report is much
bigger than the accession-only lookup that downloads use internally
(`returned_content` `ASSM_ACC`; `AssemblyReports` asks for `COMPLETE`), so
expect more data when a BioProject expands to many assemblies.

### ValidateZip: is this file a good zip archive?

```go
err := ncbigenome.ValidateZip("TAIR10.1.zip", true)
```

`ValidateZip(path, checkContents)` checks that a file is a readable zip archive:

- `checkContents: false` reads the zip directory and opens each entry without
  reading its data. It is fast and catches non-zip or truncated files (the
  directory sits at the end of a zip, so a cut-off download usually fails here).
- `checkContents: true` also reads every entry to the end, which makes Go verify
  each entry's CRC-32 checksum. This catches corrupted data but is slow for big
  packages, because it decompresses everything (the TAIR10.1 package holds about
  577 MB uncompressed, in a 60 MB zip).

`DownloadToFile` calls it automatically on the temporary file before renaming it
into place; `checkContents` there is the opposite of `Request.SkipZipContentCheck`.
Call it yourself to check packages you downloaded earlier, copied around or got
from somewhere else.

What it does not check: that the files you expect are inside, or that the
contents match NCBI's own checksums. Packages include an `md5sum.txt`, which
`ValidateZip` does not read.

### Manifest: what is in the download

`Result.Manifest` lists every file in the package: its path inside the zip, its
uncompressed and compressed size, and its CRC-32 (directory entries are left
out). `DownloadToFile` always fills it. For the `TAIR10.1.zip` test download
(`GCF_000001735.4` with `genome`, `gff3` and `seq-report`):

```go
for _, f := range res.Manifest.Files {
    fmt.Printf("%12d  %s\n", f.Size, f.Name)
}
fmt.Println(res.Manifest.TotalSize(), "bytes uncompressed")
```

```
        1593  README.md
        2421  ncbi_dataset/data/assembly_data_report.jsonl
   121164952  ncbi_dataset/data/GCF_000001735.4/GCF_000001735.4_TAIR10.1_genomic.fna
   456064451  ncbi_dataset/data/GCF_000001735.4/genomic.gff
        2152  ncbi_dataset/data/GCF_000001735.4/sequence_report.jsonl
         708  ncbi_dataset/data/dataset_catalog.json
         427  md5sum.txt
577236704 bytes uncompressed
```

(the zip itself is 59,698,394 bytes of compressed data). The manifest marshals to
JSON with the keys `files`, `name`, `size`, `compressed_size` and `crc32`, so you
can store it next to your other metadata.

**With a `Writer`.** `Download(ctx, req, w)` leaves `Result.Manifest` nil unless
you set `Request.Manifest`. A zip keeps its file list at the very end of the
archive, and the sizes in front of each entry can be missing until after its
data, so nothing can be listed while the package streams into a plain
`io.Writer`. With `Manifest: true` the library therefore also copies the stream
to a temporary file (`Options.TempDir`, default the system temp directory), reads
the list from that when the download is complete, and deletes it. That costs a
second write and disk space equal to the package size. If that copy cannot be
read as a zip, `Download` returns an error even though `w` already received the
data.

Cheaper options: if you write to a file anyway, use `DownloadToFile`, which has
the finished file already; for a zip you already have, call
`ReadManifest(path)`, which reads only the archive's file list and is fast even
for large packages.

### Try the example

    go run ./examples/fetch --include genome,gff3,seq-report --filename TAIR10.1.zip GCF_000001735.4
    go run ./examples/reports GCF_000001735.4 | jq .

## Report types and JSON Schema

`AssemblyReports` returns `AssemblyDataReport` values, NCBI's genome assembly
report. NCBI defines that report in the OpenAPI spec of its `datasets` project;
one generator script turns that definition into the Go types and the JSON
Schemas below, so they cannot drift apart.

| What | File |
|---|---|
| Go types (29 structs, 6 string enums) | `report_types.go` (generated), plus the hand-written `Uint64` in `uint64.go` |
| JSON Schema, snake_case names: REST API and `datasets summary` output | `schema/assembly-data-report.schema.json` |
| JSON Schema, camelCase names: `assembly_data_report.jsonl` inside a data package | `schema/assembly-data-report.package.schema.json` |
| Generator | `tools/genreport/generate.py` |

The Go types match the snake_case form. The `assembly_data_report.jsonl` file in
a downloaded package uses camelCase, so it does not decode into these types
directly. NCBI sends 64-bit counts (`total_sequence_length`, `atgc_count`, ...)
as JSON strings; the Go type `Uint64` reads either form and writes the string
form. Enums are plain strings in Go, so values NCBI adds later still decode.
These are our derivations from NCBI's OpenAPI definition, not official NCBI
schemas; see [schema/README.md](schema/README.md) for what they do and do not
check.

Each schema has an `$id`: a raw.githubusercontent.com URL pinned to a release tag of
this repository (for example `v0.2.0`), so the schema a record points to never
changes after the release. It resolves once that tag is pushed. A marshalled
`AssemblyDataReport` starts with a `"$schema"` property holding the snake_case
schema's `$id` (`AssemblyDataReportSchemaURL`), so stored or exchanged records
say what they conform to. Decoding ignores that property, and nested values do
not carry it. The schemas declare it as an optional property.

## Request fields and the CLI flags they replace

| CLI flag | Library |
|---|---|
| `<accession>...` (the CLI accepts several) | `Request.Accessions`: exactly one assembly accession for downloads, any number for `AssemblyReports` |
| `--assembly-version` | `Request.AssemblyVersion` (`ParseAssemblyVersion`) |
| `--include` | `Request.Include` (`ParseIncludes`) |
| `--chromosomes` | `Request.Chromosomes` (passed through unchanged, so `all` is interpreted by the API, as in the CLI) |
| `--dehydrated` | `Request.Dehydrated` |
| `--fast-zip-validation` | `Request.SkipZipContentCheck` |
| `--reference`, `--annotated`, `--exclude-atypical`, `--exclude-multi-isolate`, `--from-type` | `Request.Filters` booleans |
| `--assembly-source`, `--assembly-level`, `--mag` | `Request.Filters` (`ParseAssemblySource`, `ParseAssemblyLevels`, `ParseMAGFilter`) |
| `--api-key` | `Options.APIKey` |
| `--filename` | the `path` argument of `DownloadToFile` |
| `--no-progressbar` | no progress bar; optional `Request.OnProgress` callback |
| `--debug` | not provided; supply your own `http.Client` |
| `--preview` | not implemented |
| `--released-after`, `--released-before`, `--search` | not implemented (they select genomes by date or text, not known accessions) |
| (no CLI equivalent) | `Request.Manifest` and `Result.Manifest`: the package's file list; see [Manifest](#manifest-what-is-in-the-download) |

The filters are sent with the accession-resolution request, exactly as the CLI
does, so they matter mostly for BioProject accessions. `examples/fetch` accepts
the same flag names.

## What it does (and where it comes from in the CLI)

| Step | CLI code | Here |
|---|---|---|
| accession syntax, upper-casing | `isValidAssemblyAcc`, `separateAccessions` | `accessions.go` |
| version policy (`.4` kept / stripped / all versions) | `AssemblyVersionFlag.UpdateForInputAccessions` | `applyVersionPolicy` |
| resolve accessions / BioProjects | `setAssemblyAccsForRequest`, `ProcessPages` | `resolve.go`: `POST /genome/dataset_report`, follows `next_page_token` |
| include list to API enum, `none` sends `[]` | `SetRequestArgs` | `buildIncludes` |
| download | `GenomeDownloader.Download` | `POST /genome/download`, streamed |
| zip check | `isValidZip` | `ValidateZip` |
| retry policy | `DefaultRetryPolicy` | transport errors, 429, 5xx except 501; 1-30 s backoff; 10 retries |

## Deliberate differences from the CLI

- Output is written to a temp file, checked, then renamed; the CLI writes to the final path and deletes it if invalid.
- Include flags are validated before any network call (the CLI validates after resolving).
- BioProjects go in their own report request; assembly accessions are sent in chunks of 1000. The CLI sends up to 2000 in one request and chunks above that. The CLI's extra count-only request (page size 1) is dropped.
- Accession syntax uses a stricter regex than the CLI's "prefix + parses as a number".
- Resolved accessions are de-duplicated.
- Report requests send `Accept: application/json` only.
- `AssemblyReports` asks for the full report (`returned_content` `COMPLETE`); downloads use the lighter accession-only content (`ASSM_ACC`), as the CLI does.
- `Result.Manifest`, the file list of the package, has no CLI equivalent.
- A download covers exactly one genome. The CLI accepts several accessions, BioProjects and taxon queries and puts all the matches into one package; here one assembly accession goes in and one genome comes out, and a request that matches several assemblies is rejected before anything is downloaded.
- Requests carry a `User-Agent` and optional `api-key`. The CLI also sends `X-Datasets-Client-*` headers identifying itself as the CLI; use `Options.Headers` if you need extras.
- After retries a 429 is returned as `*APIError` with a hint (the CLI reports "too large for direct download" for 429).

## Not included

Taxon queries, `--preview` download summaries, assembly filters (reference-only,
assembly level, dates, ...), `rehydrate` (a dehydrated package can be requested
but its files are not fetched), and the gene, virus and taxonomy commands.

## Development

`make help` lists the targets:

| Target | What it does |
|---|---|
| `make test` | runs all tests (`go test ./...`); extra flags: `make test TESTFLAGS=-v` |
| `make vet` | runs `go vet ./...` |
| `make build` | compiles the library package (a library produces no file, so this checks that it builds) |
| `make build-example` | builds the two example CLIs: `bin/fetch` (download) and `bin/reports` (prints what `AssemblyReports` returns as JSON) |
| `make test-download` | builds the examples, then downloads one genome (exactly one assembly accession) with `bin/fetch` into `bin/` (**live NCBI API**). Try `make test-download ACCESSION=GCF_000001735.4 FETCH_FLAGS='--include all'` |
| `make test-accession` | builds the examples, then prints the assembly reports `AssemblyReports` returns as pretty JSON through `jq` (**live NCBI API**, needs `jq`). `ACCESSION` may hold several space-separated accessions; `REPORTS_FLAGS` takes the selection flags |
| `make all` | both builds |
| `make clean` | removes `bin/` |

Needs Go 1.21 or newer (see `go.mod`).

`make test` is offline (it uses mock servers). The two `test-` targets call the
real NCBI service, so they need network access, and they use `NCBI_API_KEY` from
your environment if it is set. `make clean` also removes the downloaded test zip.

## Status

Written by reading the CLI source and the generated client's models; request
and response shapes were taken from those models, not guessed.

- `make build`, `make build-example` and `make test` pass on macOS (arm64),
  checked on 2026-10-17. The tests run against mock servers, so they need no
  network.
- A live download of `GCF_000001735.4` (genome, GFF3 and sequence report) worked
  with an earlier version of the library.
- Not yet confirmed against the live NCBI API: the later additions, namely the
  full reports from `AssemblyReports` (`returned_content` `COMPLETE`), the
  manifest and the single-genome rule. Try `make test-accession` and
  `make test-download`.

## License

MIT, see [LICENSE](LICENSE).

## Acknowledgements

This library reimplements part of the `datasets` command-line tool from NCBI
Datasets (https://github.com/ncbi/datasets), whose source is released by NCBI
under a public domain notice that asks users to cite NCBI. If you use NCBI
Datasets or work based on it, please cite:

O'Leary NA, Cox E, Holmes JB, et al. Exploring and retrieving sequence and
metadata for species across the tree of life with NCBI Datasets. Sci Data.
2024;11(1):732. doi:10.1038/s41597-024-03571-y

Data downloaded through NCBI Datasets may carry its own usage terms; check
NCBI's data policies for the genomes you redistribute.
