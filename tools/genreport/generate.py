#!/usr/bin/env python3
"""Generate JSON Schemas and Go types for the NCBI Datasets genome assembly data report.

Reads the OpenAPI 3.0 spec of github.com/ncbi/datasets (datasets.openapi.yaml)
and writes, below --out:

  schema/assembly-data-report.schema.json          snake_case property names
                                                   (REST API, `datasets summary` output)
  schema/assembly-data-report.package.schema.json  camelCase property names
                                                   (assembly_data_report.jsonl in data packages)
  report_types.go                                  Go types for the snake_case form

Each schema gets an "$id" (the base URL, from --version or --schema-base-url, plus the file name). The Go type
AssemblyDataReport marshals with a "$schema" property holding the snake_case
schema's $id.

Usage:
  python3 tools/genreport/generate.py --spec PATH/datasets.openapi.yaml \\
      --commit <git commit of the ncbi/datasets checkout> --version vX.Y.Z [--out .]

--version is the release tag the schemas will be published under (for example v0.2.0). It sets
every "$id" and the Go constant to the raw.githubusercontent.com URLs of that tag. Regenerate with
the version of a release before tagging it. Use --schema-base-url URL instead of --version to
publish the schemas somewhere else.

Requires PyYAML. The script stops on any spec construct it does not know, so a
changed spec cannot be converted silently and wrongly.
"""
import argparse
import hashlib
import json
import os
import re
import sys

import yaml

ROOT = "v2reportsAssemblyDataReport"
PREFIX = "v2reports"
KNOWN_KEYWORDS = {"type", "title", "$ref", "format", "enum", "default", "items", "properties"}
KNOWN_TYPES = {"object", "string", "integer", "number", "boolean", "array"}
KNOWN_FORMATS = {"float", "uint64"}
# The $id of every schema is pinned to a release tag, so the schema that a record points to never
# changes after the release. --version only accepts release tags, never a branch such as "main".
SCHEMA_BASE_TEMPLATE = "https://raw.githubusercontent.com/ivcap-works/ivcap-ncbi-genome-download/{version}/schema/"
VERSION_PATTERN = r"v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?"
SNAKE_FILE = "assembly-data-report.schema.json"
PACKAGE_FILE = "assembly-data-report.package.schema.json"
SCHEMA_PROPERTY = {
    "type": "string",
    "format": "uri",
    "description": "URL of the JSON Schema this record conforms to. Written by the ncbigenome Go library when it "
                   "marshals a report; not part of NCBI's report definition.",
}
INITIALISMS = {"id": "ID", "url": "URL", "ani": "ANI", "wgs": "WGS", "gc": "GC", "atgc": "ATGC", "busco": "BUSCO"}


def die(msg):
    sys.exit("generate.py: " + msg)


def short(name):
    if not name.startswith(PREFIX):
        die("unexpected definition name " + name)
    return name[len(PREFIX):]


def ref_name(ref):
    return short(ref.rsplit("/", 1)[1])


def reachable(schemas):
    seen, stack = set(), [ROOT]
    while stack:
        n = stack.pop()
        if n in seen:
            continue
        seen.add(n)
        for ref in re.findall(r"#/components/schemas/([A-Za-z0-9_.\-]+)", json.dumps(schemas[n])):
            stack.append(ref)
    return seen


def check(node, where):
    unknown = set(node) - KNOWN_KEYWORDS
    if unknown:
        die(f"{where}: unsupported keywords {sorted(unknown)}")
    if "type" in node and node["type"] not in KNOWN_TYPES:
        die(f"{where}: unsupported type {node['type']}")
    if "format" in node and node["format"] not in KNOWN_FORMATS:
        die(f"{where}: unsupported format {node['format']}")
    for k, v in node.get("properties", {}).items():
        check(v, f"{where}.{k}")
    if "items" in node:
        check(node["items"], where + "[]")


# ---------------------------------------------------------------- JSON Schema

def camel(name):
    """protobuf JSON name: drop underscores, upper-case the following letter."""
    out, up = [], False
    for ch in name:
        if ch == "_":
            up = True
        elif up:
            out.append(ch.upper())
            up = False
        else:
            out.append(ch)
    return "".join(out)


def convert(node, rename):
    out = {}
    for k, v in node.items():
        if k == "$ref":
            out[k] = "#/$defs/" + ref_name(v)
        elif k == "properties":
            out[k] = {rename(p): convert(s, rename) for p, s in v.items()}
        elif k == "items":
            out[k] = convert(v, rename)
        else:
            out[k] = v
    if out.get("type") == "string" and out.get("format") == "uint64":
        out["pattern"] = "^[0-9]+$"
    return out


def build_schema(schemas, names, rename, naming, comment, schema_id):
    root = convert(schemas[ROOT], rename)
    root.pop("title", None)
    props = {"$schema": dict(SCHEMA_PROPERTY)}
    props.update(root["properties"])
    root["properties"] = props
    doc = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": schema_id,
        "$comment": comment,
        "title": "AssemblyDataReport",
        "description": f"NCBI Datasets genome assembly data report ({naming}). One record per genome assembly.",
    }
    doc.update(root)
    doc["$defs"] = {short(n): convert(schemas[n], rename) for n in sorted(names) if n != ROOT}
    return doc


# ------------------------------------------------------------------------- Go

def pascal(value):
    return "".join(p[:1].upper() + p[1:].lower() for p in value.split("_") if p)


def field_name(prop):
    return "".join(INITIALISMS.get(p, p[:1].upper() + p[1:]) for p in prop.split("_") if p)


def clean(text):
    return " ".join(str(text).split())


def go_scalar(p, where):
    t, f = p.get("type"), p.get("format")
    if t == "string":
        return "Uint64" if f == "uint64" else "string"
    if t == "integer":
        return "int64"
    if t == "number":
        return "float64"
    if t == "boolean":
        return "bool"
    die(f"{where}: no Go mapping for {p}")


def go_type(p, enums, where):
    if "$ref" in p:
        n = ref_name(p["$ref"])
        return n if n in enums else "*" + n
    if p.get("type") == "array":
        it = p["items"]
        if "$ref" in it:
            return "[]" + ref_name(it["$ref"])
        return "[]" + go_scalar(it, where + "[]")
    return go_scalar(p, where)


def go_struct(n, d, enums):
    lines = [f"// {n} mirrors the {PREFIX}{n} schema of the NCBI Datasets OpenAPI spec.",
             f"type {n} struct {{"]
    used = set()
    first = True
    for prop, p in d["properties"].items():
        fn = field_name(prop)
        if fn in used:
            die(f"{n}: duplicate Go field name {fn}")
        used.add(fn)
        if not first:
            lines.append("")
        first = False
        title = clean(p.get("title", ""))
        if title:
            lines.append(f"\t// {title}")
        lines.append(f'\t{fn} {go_type(p, enums, n + "." + prop)} `json:"{prop},omitempty"`')
    lines.append("}")
    return "\n".join(lines)


def go_enum(n, d, used_consts):
    lines = [f"// {n} mirrors the {PREFIX}{n} enum of the NCBI Datasets OpenAPI spec.",
             "// It is a plain string, so values the API adds later still decode.",
             f"type {n} string", "", "const ("]
    consts = []
    for v in d["enum"]:
        p = pascal(v)
        c = p if p.startswith(n) else n + p
        if c in used_consts:
            die(f"duplicate Go constant {c}")
        used_consts.add(c)
        consts.append((c, v))
    w = max(len(c) for c, _ in consts)
    for c, v in consts:
        lines.append(f'\t{c:<{w}} {n} = "{v}"')
    lines.append(")")
    return "\n".join(lines)


def go_root_extras(schema_url):
    return (
        "// AssemblyDataReportSchemaURL is the $id of the JSON Schema for the JSON form of\n"
        "// AssemblyDataReport (schema/assembly-data-report.schema.json in the repository).\n"
        f'const AssemblyDataReportSchemaURL = "{schema_url}"\n'
        "\n"
        '// MarshalJSON writes the report with a "$schema" property that points at its JSON\n'
        "// Schema, so stored or exchanged records say what they conform to. Decoding\n"
        "// ignores that property, and only the report itself carries it.\n"
        "func (r AssemblyDataReport) MarshalJSON() ([]byte, error) {\n"
        "\ttype plain AssemblyDataReport\n"
        "\treturn json.Marshal(struct {\n"
        '\t\tSchema string `json:"$schema"`\n'
        "\t\tplain\n"
        "\t}{AssemblyDataReportSchemaURL, plain(r)})\n"
        "}"
    )


def build_go(schemas, names, commit, sha, schema_url):
    enums = {short(n) for n in names if "enum" in schemas[n]}
    used_consts = set()
    blocks = []
    for n in sorted(names, key=short):
        s = short(n)
        if s in enums:
            blocks.append(go_enum(s, schemas[n], used_consts))
        else:
            block = go_struct(s, schemas[n], enums)
            if n == ROOT:
                block += "\n\n" + go_root_extras(schema_url)
            blocks.append(block)
    header = (
        "// Code generated by tools/genreport/generate.py; DO NOT EDIT.\n"
        "//\n"
        f"// Source: datasets.openapi.yaml of github.com/ncbi/datasets (commit {commit}, sha256 {sha}).\n"
        "// Derived from NCBI's OpenAPI definition; not an official NCBI package.\n"
        "\n"
        "package ncbigenome\n"
        "\n"
        'import "encoding/json"\n'
    )
    return header + "\n" + "\n\n".join(blocks) + "\n"


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    print("wrote", path)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--spec", required=True, help="path to datasets.openapi.yaml")
    ap.add_argument("--commit", default="unknown", help="git commit of the ncbi/datasets checkout")
    ap.add_argument("--version",
                    help="release tag the schemas are published under, for example v0.2.0 (sets every $id)")
    ap.add_argument("--schema-base-url",
                    help="publish the schemas under this URL instead of a release tag (sets every $id)")
    ap.add_argument("--out", default=".", help="repository root to write into")
    a = ap.parse_args()
    if bool(a.version) == bool(a.schema_base_url):
        ap.error("give exactly one of --version and --schema-base-url")
    if a.version and not re.fullmatch(VERSION_PATTERN, a.version):
        ap.error(f"--version must be a release tag such as v0.2.0, not {a.version!r}")

    raw = open(a.spec, "rb").read()
    sha = hashlib.sha256(raw).hexdigest()
    schemas = yaml.safe_load(raw)["components"]["schemas"]
    names = reachable(schemas)
    for n in sorted(names):
        check(schemas[n], n)

    comment = (
        "Generated by tools/genreport/generate.py from datasets.openapi.yaml of github.com/ncbi/datasets "
        f"(commit {a.commit}, sha256 {sha}). Derived from NCBI's OpenAPI definition; not an official NCBI "
        "schema. Additions to the source definition: uint64 strings carry the pattern ^[0-9]+$, and the root "
        'declares an optional "$schema" property.'
    )
    base = a.schema_base_url or SCHEMA_BASE_TEMPLATE.format(version=a.version)
    base = base if base.endswith("/") else base + "/"
    snake_id, package_id = base + SNAKE_FILE, base + PACKAGE_FILE

    def js(doc):
        return json.dumps(doc, indent=2, ensure_ascii=False) + "\n"

    write(os.path.join(a.out, "schema", SNAKE_FILE),
          js(build_schema(schemas, names, lambda p: p, "snake_case names, as returned by the REST API and `datasets summary`", comment, snake_id)))
    write(os.path.join(a.out, "schema", PACKAGE_FILE),
          js(build_schema(schemas, names, camel, "camelCase names, as in assembly_data_report.jsonl of a data package", comment, package_id)))
    write(os.path.join(a.out, "report_types.go"), build_go(schemas, names, a.commit, sha, snake_id))
    print(f"{len(names)} definitions converted")


if __name__ == "__main__":
    main()
