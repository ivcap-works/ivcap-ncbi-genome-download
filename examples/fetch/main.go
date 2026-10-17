// Command fetch demonstrates the ncbigenome library with the same flag names as
// "datasets download genome accession":
//
//	go run ./examples/fetch --include genome,gff3,seq-report --filename TAIR10.1.zip GCF_000001735.4
//
// It downloads exactly one genome, so it takes exactly one assembly accession.
// Flags must come before the accession.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	ncbigenome "github.com/ivcap-works/ivcap-ncbi-genome-download"
)

func main() {
	include := flag.String("include", "genome", "comma-separated: genome,rna,protein,cds,gff3,gtf,gbff,seq-report,all,none")
	filename := flag.String("filename", "ncbi_dataset.zip", "output zip file")
	chroms := flag.String("chromosomes", "", "comma-separated chromosome names, or 'all'")
	dehydrated := flag.Bool("dehydrated", false, "request a dehydrated package (files are not fetched)")
	fastZip := flag.Bool("fast-zip-validation", false, "only check the zip directory, not every entry")
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
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: fetch [flags] ACCESSION   (exactly one assembly accession, e.g. GCF_000001735.4)")
		os.Exit(2)
	}

	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	inc, err := ncbigenome.ParseIncludes(*include)
	if err != nil {
		fail(err)
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
		Include:             inc,
		Dehydrated:          *dehydrated,
		SkipZipContentCheck: *fastZip,
		OnProgress:          func(n int64) { fmt.Fprintf(os.Stderr, "\r%d bytes", n) },
	}
	if *chroms != "" {
		req.Chromosomes = strings.Split(*chroms, ",")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := ncbigenome.New(ncbigenome.Options{
		APIKey:    *apiKey,
		OnMessage: func(m string) { fmt.Fprintln(os.Stderr, "NCBI:", m) },
	})
	res, err := client.DownloadToFile(ctx, req, *filename)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if res.Warning != "" {
		fmt.Fprintln(os.Stderr, "warning:", res.Warning)
	}
	fmt.Printf("wrote %s (%d bytes) for %s\n", res.Path, res.Bytes, res.Accession)
	if res.Manifest != nil {
		fmt.Println("files in the package (uncompressed bytes):")
		for _, f := range res.Manifest.Files {
			fmt.Printf("%12d  %s\n", f.Size, f.Name)
		}
	}
}
