// Package ncbigenome downloads NCBI Datasets genome data packages by assembly
// accession. It is a library extraction of what
//
//	datasets download genome accession <ACCESSION...> [--include ...]
//
// does against the NCBI Datasets v2 REST API:
//
//  1. validate the accessions and apply the assembly-version policy,
//  2. resolve them with POST /genome/dataset_report (paged), which also expands
//     BioProject accessions and unversioned accessions to concrete assemblies,
//  3. request the package with POST /genome/download and stream the zip,
//  4. check that the result is a readable zip archive.
//
// A download covers exactly one genome: it takes one assembly accession, and a
// request that matches more than one assembly is rejected before anything is
// downloaded (ErrNotSingleGenome). AssemblyReports, which only looks things up,
// takes any number of accessions.
//
// It depends only on the Go standard library. Taxon queries, previews,
// rehydration and the gene, virus and taxonomy commands are deliberately not
// included.
package ncbigenome
