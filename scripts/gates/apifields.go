package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// The API-fields gate is the coverage gate one level down: it holds the
// gap between the fields Google publishes and the fields this server
// models to being a set of decisions rather than an accident.
//
// The coverage gate watches methods. Nothing watched fields, and a wire
// type that silently lacks a field is a bug that looks like an empty
// response: a sibling server spent a live-verification spike finding
// that out, and found its types forty fields behind the discovery
// document, thirty-nine of which nobody had ever counted.
//
// Two files, split by which of them a person writes:
//
//   - testdata/api-fields.json is the snapshot: every schema of every
//     API this server can reach, with the properties it publishes, as
//     published on the day it was fetched. `gates api-diff` writes it.
//     Nobody edits it by hand.
//   - testdata/api-fields.tsv is the record: one verdict per exception.
//     `out` is a published property this server deliberately does not
//     model, `extra` a field it models that public discovery does not
//     publish, `alias` a schema modelled by a struct of another name,
//     `owner` which API a struct models when two of them publish a
//     schema of the same name, and `unrelated` that a struct sharing a
//     schema's name is about something else entirely.
//
// Both directions fail, and the number of schemas actually compared is
// part of the rule: a gate that matches a struct to a schema by name
// goes blind the moment somebody renames a struct, and would report
// that as nothing to do.
const (
	fieldsSnapshotFile = "testdata/api-fields.json"
	fieldsRecordFile   = "testdata/api-fields.tsv"
	// fieldsWireDir is the package held to the discovery documents.
	fieldsWireDir = "internal/gchat"
)

type fieldsSnapshot struct {
	Fetched string      `json:"fetched"`
	APIs    []fieldsAPI `json:"apis"`
}

type fieldsAPI struct {
	API       string         `json:"api"`
	Discovery string         `json:"discovery"`
	Schemas   []fieldsSchema `json:"schemas"`
}

type fieldsSchema struct {
	Name       string   `json:"name"`
	Properties []string `json:"properties"`
}

// fieldsRow is one line of the record. Property is "*" for a row about a
// whole schema.
type fieldsRow struct {
	API, Schema, Property, Verdict, Reason string
	line                                   int
}

func (r fieldsRow) where() string {
	return fmt.Sprintf("%s:%d: %s %s.%s", fieldsRecordFile, r.line, r.API, r.Schema, r.Property)
}

// apiFields holds the record to the snapshot and to the wire package.
func apiFields(_ []string, stdout, stderr io.Writer) int {
	rows, problems := readFieldsRecord(fieldsRecordFile)
	if len(problems) > 0 {
		return reportProblems(stderr, problems)
	}
	snap, err := readFieldsSnapshot(fieldsSnapshotFile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	modelled, err := fieldsWireStructs(fieldsWireDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	problems, matched, off := fieldsProblems(snap, rows, modelled)
	// No floor on the number of schemas matched. There was one, at 20
	// against a real 49, which is another way of saying whole families of
	// types could leave the comparison before it complained.
	// unmatchedStructs judges every struct instead, so a rename fails on
	// the renamed type.
	if len(problems) > 0 {
		// One collision is reached once per API publishing the name, so
		// the same sentence arrives twice; a reader needs it once.
		problems = slices.Compact(slices.Sorted(slices.Values(problems)))
		return reportProblems(stderr, problems)
	}
	_, _ = fmt.Fprintf(stdout, "api fields ok (%d schemas matched in %s, %d properties left out on purpose; snapshot fetched %s)\n",
		matched, fieldsWireDir, off, snap.Fetched)
	return 0
}

// fieldsProblems is the rule, in two halves: what the record says on
// its own, then the record against the snapshot and the package. It
// returns the problems, how many schemas were compared, and how many
// properties are deliberately out.
func fieldsProblems(snap fieldsSnapshot, rows []fieldsRow, modelled map[string]map[string]bool) ([]string, int, int) {
	// published is keyed by API and schema, because two of the APIs this
	// server reaches publish schemas of the same name — merging them
	// invents both missing fields and unpublished ones.
	published := map[string][]string{}
	names := map[string][]string{} // schema name -> the api:schema keys publishing it
	for _, a := range snap.APIs {
		for _, s := range a.Schemas {
			key := a.API + ":" + s.Name
			published[key] = s.Properties
			names[s.Name] = append(names[s.Name], key)
		}
	}
	if len(published) == 0 {
		return []string{fieldsSnapshotFile + " lists no schemas; that is not a reading of the discovery documents"}, 0, 0
	}
	rec, problems := readDecisions(published, names, rows, modelled)
	compared, matched := compareFields(published, names, rec, modelled)
	problems = append(problems, compared...)
	problems = append(problems, unmatchedStructs(published, rec, modelled)...)
	return problems, matched, rec.out
}

// decisions are the exceptions the record declares, once it has been
// checked for making sense on its own.
type decisions struct {
	alias     map[string]string // api:schema -> struct name
	owner     map[string]string // schema name -> the api whose schema a struct models
	unrelated map[string]bool   // api:schema this package does not model at all
	// accountedFor is a struct in the wire package that some row
	// explains: an alias row names it as what models a schema, or a local
	// row says it models none. The third direction asks one question of
	// this set.
	accountedFor map[string]bool
	rows         []fieldsRow
	out          int
}

// has reports whether the record writes off one property of one schema.
func (d decisions) has(key, prop, kind string) bool {
	for _, r := range d.rows {
		if r.API+":"+r.Schema == key && r.Property == prop && r.Verdict == kind {
			return true
		}
	}
	return false
}

// readDecisions validates every row against the snapshot and collects
// what the rows decide.
func readDecisions(published map[string][]string, names map[string][]string, rows []fieldsRow,
	modelled map[string]map[string]bool) (decisions, []string) {
	d := decisions{
		alias: map[string]string{}, owner: map[string]string{}, unrelated: map[string]bool{},
		accountedFor: map[string]bool{},
	}
	var problems []string
	seen := map[string]int{}
	for _, r := range rows {
		key := r.API + ":" + r.Schema
		dedupe := key + "." + r.Property
		if first, dup := seen[dedupe]; dup {
			problems = append(problems, fmt.Sprintf("%s already has a verdict on line %d", r.where(), first))
			continue
		}
		seen[dedupe] = r.line
		// local is the one verdict whose first column names a struct in
		// the wire package rather than a published schema, so it is also
		// the one that is fine with a name the snapshot does not carry.
		if _, ok := published[key]; !ok && r.Verdict != "local" {
			problems = append(problems, r.where()+" is not a published schema any more; drop the row")
			continue
		}
		if strings.TrimSpace(r.Reason) == "" {
			problems = append(problems, fmt.Sprintf("%s is %s with no reason given", r.where(), r.Verdict))
			continue
		}
		if p := rowProblem(r, names, modelled); p != "" {
			problems = append(problems, p)
			continue
		}
		// Only rows that survived the checks above. Keeping a rejected one
		// would let an invalid out row excuse a field nothing models, and
		// the gate would then report the row's own fault and nothing else.
		d.rows = append(d.rows, r)
		switch r.Verdict {
		case "alias":
			d.alias[key] = r.Reason
			d.accountedFor[r.Reason] = true
		case "local":
			d.accountedFor[r.Schema] = true
		case "owner":
			d.owner[r.Schema] = r.API
		case "unrelated":
			d.unrelated[key] = true
		case "out":
			d.out++
		}
	}
	return d, problems
}

// rowProblem is what is wrong with one row's verdict, or "".
// unmatchedStructs is the third direction, over the wire package rather
// than the snapshot.
//
// Without it a gate built on a name match goes blind exactly when it
// matters: a struct renamed out of the way matches no schema, so its
// fields simply stop being compared and the gate reports success. A
// floor under the number of schemas matched was standing in for this and
// could not do the job — it sat at 20 against a real 49. Verified by
// renaming a modelled struct: the count fell by one and the gate still
// said ok.
func unmatchedStructs(published map[string][]string, d decisions, modelled map[string]map[string]bool) []string {
	names := map[string]bool{}
	for key := range published {
		_, name, _ := strings.Cut(key, ":")
		names[name] = true
	}
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(modelled)) {
		if len(modelled[name]) == 0 {
			// No JSON tag on any field: not a wire type at all.
			continue
		}
		if names[name] || d.accountedFor[name] {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s: %s is a struct in %s and no schema of that name is published; alias the schema it models to it, "+
				"or add a local row saying it models none", fieldsRecordFile, name, fieldsWireDir))
	}
	return problems
}
func rowProblem(r fieldsRow, names map[string][]string, modelled map[string]map[string]bool) string {
	if r.Verdict == "local" {
		switch {
		case r.Property != "*":
			return r.where() + " is local as a whole type, so its property must be *"
		case len(names[r.Schema]) > 0:
			return r.where() + " names a published schema, so it is not local; drop the row"
		case modelled[r.Schema] == nil:
			return fmt.Sprintf("%s is written off as local and is not a struct in %s; drop the row", r.where(), fieldsWireDir)
		}
		return ""
	}
	switch r.Verdict {
	case "alias", "owner", "unrelated":
		if r.Property != "*" {
			return r.where() + " is a verdict about a whole schema, so its property must be *"
		}
		switch {
		case r.Verdict == "owner" && len(names[r.Schema]) < 2:
			return fmt.Sprintf("%s claims ownership of a schema name only %s publishes; an owner row is for a name two APIs share",
				r.where(), r.API)
		case r.Verdict == "alias" && modelled[r.Reason] == nil:
			// An alias's reason is a struct name, the way a used row's
			// reason in the coverage record is a method name.
			return fmt.Sprintf("%s is aliased to %q, which is not a struct in %s", r.where(), r.Reason, fieldsWireDir)
		}
		return ""
	case "out", "extra":
		if r.Property == "*" {
			return r.where() + " is a per-property verdict, so its property cannot be *"
		}
		return ""
	}
	return fmt.Sprintf("%s: verdict %q is none of out, extra, alias, local, owner or unrelated", r.where(), r.Verdict)
}

// structFor is the struct modelling one published schema, and whether
// there is one to compare at all.
func structFor(key, schema string, names map[string][]string, d decisions, modelled map[string]map[string]bool) (string, string, bool) {
	if a, ok := d.alias[key]; ok {
		return a, "", true
	}
	api, _, _ := strings.Cut(key, ":")
	if shared := names[schema]; len(shared) > 1 {
		// Two APIs publish this name. Which one the struct models is a
		// fact only a person knows, and guessing it is how a gate comes
		// to report a field as missing from a type never about that API.
		if _, ok := modelled[schema]; !ok {
			return "", "", false
		}
		if d.owner[schema] == "" {
			return "", fmt.Sprintf(
				"%s: %s is published by %s and %s models a struct of that name; add an owner row saying which API it models",
				fieldsRecordFile, schema, strings.Join(shared, " and "), fieldsWireDir), false
		}
		if d.owner[schema] != api {
			return "", "", false
		}
	}
	return schema, "", true
}

// compareFields holds every schema this package models to what its API
// publishes, in both directions.
func compareFields(published map[string][]string, names map[string][]string, d decisions, modelled map[string]map[string]bool) ([]string, int) {
	var problems []string
	matched := 0
	for _, key := range slices.Sorted(mapKeys(published)) {
		if d.unrelated[key] {
			continue
		}
		api, schema, _ := strings.Cut(key, ":")
		name, problem, ok := structFor(key, schema, names, d, modelled)
		if problem != "" {
			problems = append(problems, problem)
		}
		if !ok {
			continue
		}
		tags, isModelled := modelled[name]
		if !isModelled {
			// A schema this package does not model at all is a question
			// about capability, which the coverage gate answers.
			continue
		}
		matched++
		for _, prop := range published[key] {
			if !tags[prop] && !d.has(key, prop, "out") {
				problems = append(problems, fmt.Sprintf(
					"%s: %s %s.%s is published and %s does not model it; add the field, or a row saying why not",
					fieldsRecordFile, api, schema, prop, name))
			}
		}
		pub := map[string]bool{}
		for _, p := range published[key] {
			pub[p] = true
		}
		for _, tag := range slices.Sorted(mapKeys(tags)) {
			if !pub[tag] && !d.has(key, tag, "extra") {
				problems = append(problems, fmt.Sprintf(
					"%s: %s.%s is modelled and %s %s does not publish it; add a row saying why it is there",
					fieldsRecordFile, name, tag, api, schema))
			}
		}
	}
	return problems, matched
}

// fieldsWireStructs is every struct in the wire package by name, with
// the JSON tags it carries. Embedded structs are resolved, because a
// promoted field is on the wire exactly as if it had been declared.
func fieldsWireStructs(dir string) (map[string]map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	direct := map[string]map[string]bool{}
	embeds := map[string][]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			tags := map[string]bool{}
			for _, f := range st.Fields.List {
				if len(f.Names) == 0 {
					if id, ok := f.Type.(*ast.Ident); ok {
						embeds[ts.Name.Name] = append(embeds[ts.Name.Name], id.Name)
					}
					continue
				}
				if tag := fieldsJSONTag(f); tag != "" {
					tags[tag] = true
				}
			}
			direct[ts.Name.Name] = tags
			return true
		})
	}
	out := make(map[string]map[string]bool, len(direct))
	for name := range direct {
		tags := map[string]bool{}
		var add func(string, map[string]bool)
		add = func(n string, visited map[string]bool) {
			if visited[n] {
				return
			}
			visited[n] = true
			for t := range direct[n] {
				tags[t] = true
			}
			for _, e := range embeds[n] {
				add(e, visited)
			}
		}
		add(name, map[string]bool{})
		out[name] = tags
	}
	return out, nil
}

// fieldsJSONTag is the field's name on the wire, or "" for a field the
// API never sees.
func fieldsJSONTag(f *ast.Field) string {
	if f.Tag == nil {
		return ""
	}
	raw := strings.Trim(f.Tag.Value, "`")
	i := strings.Index(raw, `json:"`)
	if i < 0 {
		return ""
	}
	rest := raw[i+len(`json:"`):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	name, _, _ := strings.Cut(rest[:j], ",")
	if name == "-" {
		return ""
	}
	return name
}

func readFieldsSnapshot(path string) (fieldsSnapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fieldsSnapshot{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	var s fieldsSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return fieldsSnapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func readFieldsRecord(path string) ([]fieldsRow, []string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("cannot read %s: %v", path, err)}
	}
	var rows []fieldsRow
	var problems []string
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			problems = append(problems, fmt.Sprintf("%s:%d: expected api, schema, property, verdict and reason separated by tabs", path, i+1))
			continue
		}
		r := fieldsRow{API: parts[0], Schema: parts[1], Property: parts[2], Verdict: parts[3], line: i + 1}
		if len(parts) > 4 {
			r.Reason = strings.TrimSpace(parts[4])
		}
		rows = append(rows, r)
	}
	return rows, problems
}

// writeFieldsSnapshot refetches the schemas and rewrites the snapshot,
// reporting what moved. Called by api-diff, the one command here that
// touches the network.
func writeFieldsSnapshot(stdout io.Writer) error {
	old, err := readFieldsSnapshot(fieldsSnapshotFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	fresh := fieldsSnapshot{Fetched: time.Now().UTC().Format("2006-01-02")}
	for _, name := range slices.Sorted(mapKeys(apis)) {
		src := apis[name]
		if src.discovery == "" {
			continue // hand-listed: no discovery document to read
		}
		schemas, err := fetchFieldsSchemas(src.discovery)
		if err != nil {
			return fmt.Errorf("%s schemas: %w", name, err)
		}
		if len(schemas) == 0 {
			return fmt.Errorf("%s published no schemas; refusing to write that", name)
		}
		fresh.APIs = append(fresh.APIs, fieldsAPI{API: name, Discovery: src.discovery, Schemas: schemas})
	}

	index := func(s fieldsSnapshot) map[string]bool {
		m := map[string]bool{}
		for _, a := range s.APIs {
			for _, sc := range a.Schemas {
				for _, p := range sc.Properties {
					m[a.API+" "+sc.Name+"."+p] = true
				}
			}
		}
		return m
	}
	before, after := index(old), index(fresh)
	var lines []string
	for _, k := range slices.Sorted(mapKeys(after)) {
		if !before[k] {
			lines = append(lines, "NEW FIELD  "+k)
		}
	}
	for _, k := range slices.Sorted(mapKeys(before)) {
		if !after[k] {
			lines = append(lines, "GONE FIELD "+k)
		}
	}
	data, err := json.MarshalIndent(fresh, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(fieldsSnapshotFile, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if len(lines) == 0 {
		_, err = fmt.Fprintf(stdout, "api diff: no field moved; %s rewritten with today's date\n", fieldsSnapshotFile)
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n\n%s rewritten. Every NEW FIELD on a schema this server models needs the field or a row in %s.\n",
		strings.Join(lines, "\n"), fieldsSnapshotFile, fieldsRecordFile)
	return err
}

// fetchFieldsSchemas reads the type half of a discovery document, where
// the coverage gate reads the method half.
func fetchFieldsSchemas(url string) ([]fieldsSchema, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery returned %s", res.Status)
	}
	var doc struct {
		Schemas map[string]discoverySchema `json:"schemas"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]fieldsSchema, 0, len(doc.Schemas))
	for name, s := range doc.Schemas {
		out = append(out, flattenSchema(name, s.Properties)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// discoverySchema is as much of a discovery document's schema as this
// gate reads: the property names, and the shape of any property that
// carries its own properties rather than a $ref.
type discoverySchema struct {
	Type       string                     `json:"type"`
	Properties map[string]discoverySchema `json:"properties"`
	Items      *discoverySchema           `json:"items"`
}

// flattenSchema is one published schema and every object defined inline
// inside it, each as a schema of its own named Parent.property.
//
// Of the seven discovery documents these four servers read, only Drive
// v3 does this today — it declares File.capabilities and twenty others as
// anonymous objects rather than as a $ref. Reading only the top level
// recorded each as a single property name, so its 166 sub-properties
// collapsed to 21 and the types modelling them matched no schema at all.
// The descent is here in every server because the omission is invisible
// until an API starts doing it, which is the same way the last one bit.
//
// Named rather than nested so that everything downstream — the alias
// rows, the per-property verdicts, both directions of the comparison —
// works on them unchanged.
func flattenSchema(name string, props map[string]discoverySchema) []fieldsSchema {
	// A schema with no properties is still a published schema, so it is
	// recorded rather than skipped.
	out := []fieldsSchema{{Name: name, Properties: slices.Sorted(mapKeys(props))}}
	for _, prop := range slices.Sorted(mapKeys(props)) {
		p := props[prop]
		// An array of inline objects is its element's shape; a $ref has
		// no properties here and is reached as its own schema.
		if p.Type == "array" && p.Items != nil {
			p = *p.Items
		}
		if p.Type == "object" && len(p.Properties) > 0 {
			out = append(out, flattenSchema(name+"."+prop, p.Properties)...)
		}
	}
	return out
}
