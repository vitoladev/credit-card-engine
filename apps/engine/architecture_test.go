package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const modulePrefix = "engine"

type Graph map[string][]string

type rule struct {
	name string
	from string
	to   []string
}

var rules = []rule{
	{name: "domain imports another internal package",
		from: "internal/domain", to: []string{"internal"}},
	{name: "rules import a module or adapter",
		from: "internal/rules", to: []string{"internal/evaluate", "internal/batch", "internal/adapter", "internal/flocitest"}},
	{name: "evaluate imports batch or an adapter",
		from: "internal/evaluate", to: []string{"internal/batch", "internal/adapter", "internal/flocitest"}},
	{name: "batch imports evaluate or an adapter",
		from: "internal/batch", to: []string{"internal/evaluate", "internal/adapter", "internal/flocitest"}},
	{name: "an adapter imports the test helper",
		from: "internal/adapter", to: []string{"internal/flocitest"}},
	{name: "adapter/httpapi imports another adapter",
		from: "internal/adapter/httpapi", to: []string{"internal/adapter"}},
	{name: "adapter/sqs imports evaluate or another adapter",
		from: "internal/adapter/sqs", to: []string{"internal/evaluate", "internal/adapter"}},
	{name: "adapter/ddb imports another adapter",
		from: "internal/adapter/ddb", to: []string{"internal/adapter/httpapi", "internal/adapter/sqs", "internal/adapter/sqspub", "internal/adapter/telemetry"}},
	{name: "adapter/sqspub imports evaluate or another adapter",
		from: "internal/adapter/sqspub", to: []string{"internal/evaluate", "internal/adapter"}},
	{name: "adapter/telemetry imports a module or another adapter",
		from: "internal/adapter/telemetry", to: []string{"internal/evaluate", "internal/batch", "internal/rules", "internal/adapter"}},
	{name: "cmd imports the test helper",
		from: "cmd", to: []string{"internal/flocitest"}},
	{name: "cmd/http imports the queue consumer",
		from: "cmd/http", to: []string{"internal/adapter/sqs"}},
	{name: "cmd/worker imports httpapi, evaluate, or the publisher",
		from: "cmd/worker", to: []string{"internal/adapter/httpapi", "internal/evaluate", "internal/adapter/sqspub"}},
	{name: "cmd/dlq imports httpapi, evaluate, rules, or the publisher",
		from: "cmd/dlq", to: []string{"internal/adapter/httpapi", "internal/evaluate", "internal/rules", "internal/adapter/sqspub"}},
}

func Violations(g Graph) []string {
	var out []string
	for pkg, imports := range g {
		from, ok := stripModule(pkg)
		if !ok {
			continue
		}
		for _, r := range rules {
			if !under(from, r.from) {
				continue
			}
			for _, imp := range imports {
				to, ok := stripModule(imp)
				if !ok {
					continue
				}
				for _, forbidden := range r.to {
					if under(to, forbidden) {
						out = append(out, from+" -> "+to+" ("+r.name+")")
					}
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

func stripModule(importPath string) (string, bool) {
	if importPath == modulePrefix {
		return "", true
	}
	return strings.CutPrefix(importPath, modulePrefix+"/")
}

func ExternalImportViolations(g Graph) []string {
	var out []string
	for pkg, imports := range g {
		from, ok := stripModule(pkg)
		if !ok {
			continue
		}
		for _, imp := range imports {
			if underAny(from, "internal/domain", "internal/rules", "internal/evaluate", "internal/batch") &&
				strings.HasPrefix(imp, "github.com/aws/") {
				out = append(out, from+" -> "+imp+" (domain, rules, and modules import AWS; ADR 0002)")
			}
		}
	}
	slices.Sort(out)
	return out
}

// under reports whether pkg is dir or a package below it.
func under(pkg, dir string) bool {
	return pkg == dir || strings.HasPrefix(pkg, dir+"/")
}

func underAny(pkg string, dirs ...string) bool {
	return slices.ContainsFunc(dirs, func(dir string) bool { return under(pkg, dir) })
}

func TestRealModuleObeysTheDAG(t *testing.T) {
	g := parseGraph(t, moduleRoot(t))
	if violations := Violations(g); len(violations) > 0 {
		t.Errorf("forbidden import edges:\n%s", strings.Join(violations, "\n"))
	}
}

func TestRealModuleHasNoForbiddenExternalImports(t *testing.T) {
	g := parseGraph(t, moduleRoot(t))
	if violations := ExternalImportViolations(g); len(violations) > 0 {
		t.Errorf("forbidden external imports:\n%s", strings.Join(violations, "\n"))
	}
}

func TestFixtureDomainImportingEvaluateFails(t *testing.T) {
	fixture := Graph{
		modulePrefix + "/internal/domain": {modulePrefix + "/internal/evaluate"},
	}
	if len(Violations(fixture)) == 0 {
		t.Fatal("expected domain -> evaluate to be reported")
	}
}

func TestFixtureBatchImportingAnAdapterFails(t *testing.T) {
	fixture := Graph{
		modulePrefix + "/internal/batch": {modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/evaluate"},
	}
	if got := len(Violations(fixture)); got != 2 {
		t.Fatalf("expected 2 violations for batch -> ddb, evaluate, got %d", got)
	}
}

func TestFixtureBatchImportingAWSFails(t *testing.T) {
	fixture := Graph{
		modulePrefix + "/internal/batch": {"github.com/aws/aws-sdk-go-v2/service/dynamodb"},
	}
	if len(ExternalImportViolations(fixture)) != 1 {
		t.Fatal("expected batch -> AWS SDK to be reported")
	}
}

func TestFixtureAllowedEdgesStayClean(t *testing.T) {
	fixture := Graph{
		modulePrefix + "/cmd/http":                   {modulePrefix + "/internal/adapter/httpapi", modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/adapter/sqspub", modulePrefix + "/internal/adapter/telemetry", modulePrefix + "/internal/batch", modulePrefix + "/internal/evaluate", modulePrefix + "/internal/rules"},
		modulePrefix + "/cmd/worker":                 {modulePrefix + "/internal/adapter/sqs", modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/adapter/telemetry", modulePrefix + "/internal/batch", modulePrefix + "/internal/rules"},
		modulePrefix + "/cmd/dlq":                    {modulePrefix + "/internal/adapter/sqs", modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/adapter/telemetry", modulePrefix + "/internal/batch"},
		modulePrefix + "/internal/adapter/httpapi":   {modulePrefix + "/internal/evaluate", modulePrefix + "/internal/batch", modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/adapter/sqs":       {modulePrefix + "/internal/batch"},
		modulePrefix + "/internal/adapter/ddb":       {modulePrefix + "/internal/batch", modulePrefix + "/internal/evaluate", modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/adapter/sqspub":    {modulePrefix + "/internal/batch"},
		modulePrefix + "/internal/adapter/telemetry": {modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/batch":             {modulePrefix + "/internal/rules", modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/evaluate":          {modulePrefix + "/internal/rules", modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/rules":             {modulePrefix + "/internal/domain"},
	}
	if violations := Violations(fixture); len(violations) > 0 {
		t.Errorf("expected no violations, got:\n%s", strings.Join(violations, "\n"))
	}
}

func parseGraph(t *testing.T, root string) Graph {
	t.Helper()
	g := make(Graph)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		importPath := path.Join(modulePrefix, filepath.ToSlash(rel))
		for _, spec := range f.Imports {
			g[importPath] = append(g[importPath], strings.Trim(spec.Path.Value, `"`))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Dir(self)
}
