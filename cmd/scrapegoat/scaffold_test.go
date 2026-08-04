package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedSpiderCompiles is the regression test for a scaffold that produced
// code which did not build.
//
// The template embedded the *generator's* own imports — golang.org/x/text/cases
// and golang.org/x/text/language — into the generated file, where nothing used
// them. So `scrapegoat new project` created a project that failed to compile on
// the very next command the tool told the user to run. Nothing caught it because
// nothing ever compiled the output.
//
// Parsing rather than building: a full `go build` needs the module resolved from
// the network, which does not belong in a unit test. Unused imports and syntax
// errors are exactly what broke, and both are visible to the parser.
func TestGeneratedSpiderCompiles(t *testing.T) {
	dir := t.TempDir()

	if err := generateSpiderInDir(dir, "example"); err != nil {
		t.Fatalf("generate spider: %v", err)
	}

	path := filepath.Join(dir, "main.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated spider: %v", err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	if err != nil {
		t.Fatalf("generated spider does not parse: %v\n\n%s", err, src)
	}

	// Every import must be referenced. This is the specific defect that shipped.
	body := string(src)
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)

		name := path
		if imp.Name != nil {
			name = imp.Name.Name
		} else if i := strings.LastIndex(path, "/"); i >= 0 {
			name = path[i+1:]
		}

		if !strings.Contains(body, name+".") {
			t.Errorf("generated spider imports %q but never uses %s — it will not compile",
				path, name)
		}
	}
}

// TestGeneratedSpiderUsesThePublicAPI guards against the scaffold drifting onto
// internal packages, which a user's project cannot import at all.
func TestGeneratedSpiderUsesThePublicAPI(t *testing.T) {
	dir := t.TempDir()
	if err := generateSpiderInDir(dir, "example"); err != nil {
		t.Fatalf("generate spider: %v", err)
	}

	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("read generated spider: %v", err)
	}

	if strings.Contains(string(src), "ScrapeGoat/internal/") {
		t.Error("generated spider imports an internal package; a user's module cannot")
	}
	if !strings.Contains(string(src), "ScrapeGoat/pkg/scrapegoat") {
		t.Error("generated spider does not import the public SDK")
	}
}

// TestGeneratedSpiderNameIsExported checks the identifier derived from the
// project name is usable — a lowercase or punctuated name would produce a type
// the template's own main function cannot reference.
func TestGeneratedSpiderNameIsExported(t *testing.T) {
	for _, name := range []string{"example", "myScraper", "books"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := generateSpiderInDir(dir, name); err != nil {
				t.Fatalf("generate: %v", err)
			}

			path := filepath.Join(dir, "main.go")
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}

			fset := token.NewFileSet()
			if _, err := parser.ParseFile(fset, path, src, parser.AllErrors); err != nil {
				t.Fatalf("does not parse for name %q: %v", name, err)
			}
		})
	}
}
