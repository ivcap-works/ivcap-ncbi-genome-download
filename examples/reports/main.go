// Command reports prints the genome assembly reports that AssemblyReports
// returns, as one JSON array on stdout. Pretty-print it with jq:
//
//	go run ./examples/reports GCF_000001735.4 | jq .
//
// It takes the same selection flags as examples/fetch. Flags must come before
// the accessions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	ncbigenome "github.com/ivcap-works/ivcap-ncbi-genome-download"
)

func main() {
	apiKey := flag.String("api-key", os.Getenv("NCBI_API_KEY"), "NCBI API key (default: $NCBI_API_KEY)")
	version := flag.String("assembly-version", "", "all|current (default: keep versions as given)")
	source := flag.String("assembly-source", "all", "all|refseq|genbank")
	levels := flag.String("assembly-level", "", "comma-separated: chromosome,complete,contig,scaffold")
	mag := flag.String("mag", "all", "all|only|exclude")
	reference := flag.Bool("reference", false, "limit to reference genomes")
	annotated := flag.Bool("annotated", false, "limit to annotated genomes")
	exclAtypical := flag.Bool("exclude-atypical", false, "exclude atypical assemblies")
	exclMulti := flag.Bool("exclude-multi-isolate", false, "exclude assemblies from multi-isolate projects")
	fromType := flag.Bool("from-type", false, "only records with type material")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: reports [flags] ACCESSION...")
		os.Exit(2)
	}

	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	ver, err := ncbigenome.ParseAssemblyVersion(*version)
	if err != nil {
		fail(err)
	}
	src, err := ncbigenome.ParseAssemblySource(*source)
	if err != nil {
		fail(err)
	}
	lvls, err := ncbigenome.ParseAssemblyLevels(*levels)
	if err != nil {
		fail(err)
	}
	magFilter, err := ncbigenome.ParseMAGFilter(*mag)
	if err != nil {
		fail(err)
	}

	req := ncbigenome.Request{
		Accessions:      flag.Args(),
		AssemblyVersion: ver,
		Filters: ncbigenome.Filters{
			ReferenceOnly:       *reference,
			AnnotatedOnly:       *annotated,
			AssemblySource:      src,
			AssemblyLevels:      lvls,
			ExcludeAtypical:     *exclAtypical,
			ExcludeMultiIsolate: *exclMulti,
			TypeMaterialOnly:    *fromType,
			MAG:                 magFilter,
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := ncbigenome.New(ncbigenome.Options{
		APIKey:    *apiKey,
		OnMessage: func(m string) { fmt.Fprintln(os.Stderr, "NCBI:", m) },
	})
	reports, err := client.AssemblyReports(ctx, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if reports == nil {
		reports = []ncbigenome.AssemblyDataReport{} // print [] rather than null
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false) // keep & in URLs readable
	if err := enc.Encode(reports); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
