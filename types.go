package ncbigenome

import (
	"fmt"
	"strings"
)

// Include names a kind of file that can be requested in a genome data package.
// The names match the values of the datasets CLI's --include flag.
type Include string

const (
	IncludeGenome    Include = "genome"     // genomic sequence (FASTA)
	IncludeRNA       Include = "rna"        // transcript sequences
	IncludeProtein   Include = "protein"    // amino acid sequences
	IncludeCDS       Include = "cds"        // nucleotide coding sequences
	IncludeGFF3      Include = "gff3"       // annotation, GFF3
	IncludeGTF       Include = "gtf"        // annotation, GTF
	IncludeGBFF      Include = "gbff"       // GenBank flat file
	IncludeSeqReport Include = "seq-report" // per-sequence report
	IncludeAll       Include = "all"        // every type above; must be used alone
	IncludeNone      Include = "none"       // metadata only; must be used alone
)

// includeWire maps Include values to the API's AnnotationForAssemblyType names.
var includeWire = map[Include]string{
	IncludeGenome:    "GENOME_FASTA",
	IncludeRNA:       "RNA_FASTA",
	IncludeProtein:   "PROT_FASTA",
	IncludeCDS:       "CDS_FASTA",
	IncludeGFF3:      "GENOME_GFF",
	IncludeGTF:       "GENOME_GTF",
	IncludeGBFF:      "GENOME_GBFF",
	IncludeSeqReport: "SEQUENCE_REPORT",
}

// allIncludes is what IncludeAll expands to (same order as the CLI).
var allIncludes = []Include{
	IncludeGenome, IncludeProtein, IncludeCDS, IncludeGFF3,
	IncludeGTF, IncludeGBFF, IncludeRNA, IncludeSeqReport,
}

// ParseInclude parses one CLI-style include name, case-insensitively.
func ParseInclude(s string) (Include, error) {
	inc := Include(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := includeWire[inc]; ok || inc == IncludeAll || inc == IncludeNone {
		return inc, nil
	}
	return "", fmt.Errorf("unknown include value %q", s)
}

// ParseIncludes parses a comma-separated list such as "genome,gff3,seq-report".
// An empty string returns nil, which Request treats as the default (genome only).
func ParseIncludes(list string) ([]Include, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []Include
	for _, part := range strings.Split(list, ",") {
		inc, err := ParseInclude(part)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, nil
}

// AssemblyVersion controls which versions of an assembly accession are returned
// (the CLI's --assembly-version).
type AssemblyVersion int

const (
	// VersionDefault keeps accessions that carry a version (GCF_000001735.4)
	// exactly as given, and resolves unversioned ones to the current version.
	VersionDefault AssemblyVersion = iota
	// VersionCurrent strips versions and returns the current version.
	VersionCurrent
	// VersionAll strips versions and returns every version of each assembly.
	VersionAll
)

// ParseAssemblyVersion parses "", "current", "latest" or "all".
func ParseAssemblyVersion(s string) (AssemblyVersion, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return VersionDefault, nil
	case "current", "latest":
		return VersionCurrent, nil
	case "all":
		return VersionAll, nil
	}
	return VersionDefault, fmt.Errorf("unknown assembly version %q (want current, latest or all)", s)
}

// AssemblySource limits results to RefSeq (GCF_) or GenBank (GCA_) assemblies.
type AssemblySource string

const (
	SourceAll     AssemblySource = "all"
	SourceRefSeq  AssemblySource = "refseq"
	SourceGenBank AssemblySource = "genbank"
)

// ParseAssemblySource parses "", "all", "refseq" or "genbank".
func ParseAssemblySource(s string) (AssemblySource, error) {
	v := AssemblySource(strings.ToLower(strings.TrimSpace(s)))
	switch v {
	case "", SourceAll:
		return SourceAll, nil
	case SourceRefSeq, SourceGenBank:
		return v, nil
	}
	return "", fmt.Errorf("unknown assembly source %q (want all, refseq or genbank)", s)
}

// AssemblyLevel is an assembly completeness level.
type AssemblyLevel string

const (
	LevelChromosome AssemblyLevel = "chromosome"
	LevelComplete   AssemblyLevel = "complete"
	LevelContig     AssemblyLevel = "contig"
	LevelScaffold   AssemblyLevel = "scaffold"
)

// levelWire maps AssemblyLevel to the API's value; note "complete" is sent as
// "complete_genome".
var levelWire = map[AssemblyLevel]string{
	LevelChromosome: "chromosome",
	LevelComplete:   "complete_genome",
	LevelContig:     "contig",
	LevelScaffold:   "scaffold",
}

// ParseAssemblyLevels parses a comma-separated list such as "complete,chromosome".
func ParseAssemblyLevels(list string) ([]AssemblyLevel, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []AssemblyLevel
	for _, part := range strings.Split(list, ",") {
		l := AssemblyLevel(strings.ToLower(strings.TrimSpace(part)))
		if _, ok := levelWire[l]; !ok {
			return nil, fmt.Errorf("unknown assembly level %q (want chromosome, complete, contig or scaffold)", part)
		}
		out = append(out, l)
	}
	return out, nil
}

// MAGFilter selects metagenome-assembled genomes (the CLI's --mag).
type MAGFilter string

const (
	MAGAll     MAGFilter = "all"     // no filtering (the zero value also means this)
	MAGOnly    MAGFilter = "only"    // only metagenome-assembled genomes
	MAGExclude MAGFilter = "exclude" // remove them
)

var magWire = map[MAGFilter]string{
	MAGAll:     "METAGENOME_DERIVED_UNSET",
	MAGOnly:    "metagenome_derived_only",
	MAGExclude: "metagenome_derived_exclude",
}

// ParseMAGFilter parses "", "all", "only" or "exclude".
func ParseMAGFilter(s string) (MAGFilter, error) {
	v := MAGFilter(strings.ToLower(strings.TrimSpace(s)))
	if v == "" {
		return MAGAll, nil
	}
	if _, ok := magWire[v]; ok {
		return v, nil
	}
	return "", fmt.Errorf("unknown MAG filter %q (want all, only or exclude)", s)
}

// Filters narrow the assemblies an accession list resolves to. In the CLI they
// are applied to the same resolution request as here, so they matter most for
// BioProject accessions that expand to many assemblies. The zero value applies
// no filtering.
type Filters struct {
	ReferenceOnly       bool            // --reference
	AnnotatedOnly       bool            // --annotated
	AssemblySource      AssemblySource  // --assembly-source (zero = all)
	AssemblyLevels      []AssemblyLevel // --assembly-level
	ExcludeAtypical     bool            // --exclude-atypical
	ExcludeMultiIsolate bool            // --exclude-multi-isolate
	TypeMaterialOnly    bool            // --from-type
	MAG                 MAGFilter       // --mag (zero = all)
}

// Request describes one genome package download. Each field notes the CLI flag
// it corresponds to.
type Request struct {
	// Accessions name the assemblies. AssemblyReports accepts any number of
	// assembly (GCA_/GCF_) or BioProject (PRJ...) accessions. Download and
	// DownloadToFile accept exactly one assembly accession: a BioProject is
	// rejected, and so is a request that matches more than one assembly (see
	// ErrNotSingleGenome).
	Accessions []string
	// AssemblyVersion selects the version policy (--assembly-version).
	AssemblyVersion AssemblyVersion
	// Filters narrow the resolved assemblies (see Filters).
	Filters Filters

	// Include selects the files in the package (--include). Nil or empty means
	// genome only, like the CLI's default. Use []Include{IncludeNone} for
	// metadata only.
	Include []Include
	// Chromosomes restricts the genome FASTA to named chromosomes
	// (--chromosomes). The values are passed to the API unchanged, so "all" is
	// interpreted by the server, as with the CLI.
	Chromosomes []string
	// Dehydrated requests a package that lists data files instead of embedding
	// them (--dehydrated). Fetching those files is not implemented here.
	Dehydrated bool

	// SkipZipContentCheck only checks the zip directory instead of reading every
	// entry (--fast-zip-validation). Used by DownloadToFile.
	SkipZipContentCheck bool
	// OnProgress, if set, is called with the running byte count while the
	// package streams in (replaces the progress bar).
	OnProgress func(written int64)
	// Manifest asks Download to fill Result.Manifest with the package's file list.
	// A zip stores that list at its end, so a plain io.Writer cannot provide it
	// while the data streams past: Download also copies the zip to a temporary
	// file (Options.TempDir, default the system temp directory), reads the list
	// from it and deletes it. If the list cannot be read, Download returns an
	// error even though the data has already been written to the writer.
	// DownloadToFile always fills Result.Manifest and ignores this field.
	Manifest bool
}

// Result describes a completed download.
type Result struct {
	// Accession is the assembly the package was downloaded for, with its version.
	// A download always covers exactly one genome.
	Accession string
	// Bytes is the size of the zip stream.
	Bytes int64
	// Path is the output file (DownloadToFile only).
	Path string
	// Warning is non-empty when the request is legal but probably not what the
	// caller meant (e.g. chromosome filter with whole-genome annotation files).
	Warning string
	// Manifest lists the files in the package. DownloadToFile always fills it;
	// Download fills it when Request.Manifest is set.
	Manifest *Manifest
}
