package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
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
	{name: "rules import a use case, store, queue, or adapter",
		from: "internal/rules", to: []string{"internal/evaluate", "internal/processjob", "internal/submit", "internal/report", "internal/queue", "internal/store", "internal/adapter"}},
	{name: "store imports rules, use case, queue, or adapter",
		from: "internal/store", to: []string{"internal/rules", "internal/evaluate", "internal/processjob", "internal/submit", "internal/report", "internal/queue", "internal/adapter"}},
	{name: "queue imports rules, use case, store, or adapter",
		from: "internal/queue", to: []string{"internal/rules", "internal/evaluate", "internal/processjob", "internal/submit", "internal/report", "internal/store", "internal/adapter"}},
	{name: "evaluate imports another use case or adapter",
		from: "internal/evaluate", to: []string{"internal/processjob", "internal/submit", "internal/report", "internal/queue", "internal/adapter"}},
	{name: "submit imports evaluate, rules, report, or adapter",
		from: "internal/submit", to: []string{"internal/evaluate", "internal/rules", "internal/report", "internal/processjob", "internal/adapter"}},
	{name: "report imports evaluate, rules, queue, submit, or adapter",
		from: "internal/report", to: []string{"internal/evaluate", "internal/rules", "internal/queue", "internal/submit", "internal/processjob", "internal/adapter"}},
	{name: "processjob imports rules, submit, report, or adapter",
		from: "internal/processjob", to: []string{"internal/rules", "internal/submit", "internal/report", "internal/adapter"}},
	{name: "adapter/httpapi imports processjob or worker adapters",
		from: "internal/adapter/httpapi", to: []string{"internal/processjob", "internal/adapter/sqs", "internal/adapter/ddb", "internal/adapter/sqspub"}},
	{name: "adapter/sqs imports submit, report, or httpapi",
		from: "internal/adapter/sqs", to: []string{"internal/submit", "internal/report", "internal/adapter/httpapi"}},
	{name: "cmd/http imports processjob or the worker adapter",
		from: "cmd/http", to: []string{"internal/processjob", "internal/adapter/sqs"}},
	{name: "cmd/worker imports submit, report, or httpapi",
		from: "cmd/worker", to: []string{"internal/submit", "internal/report", "internal/adapter/httpapi", "internal/adapter/sqspub"}},
}

func Violations(g Graph) []string {
	var out []string
	for pkg, imports := range g {
		from, ok := stripModule(pkg)
		if !ok {
			continue
		}
		for _, r := range rules {
			if from != r.from && !strings.HasPrefix(from, r.from+"/") {
				continue
			}
			for _, imp := range imports {
				to, ok := stripModule(imp)
				if !ok {
					continue
				}
				for _, forbidden := range r.to {
					if to == forbidden || strings.HasPrefix(to, forbidden+"/") {
						out = append(out, from+" -> "+to+" ("+r.name+")")
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func stripModule(importPath string) (string, bool) {
	if importPath == modulePrefix {
		return "", true
	}
	if !strings.HasPrefix(importPath, modulePrefix+"/") {
		return "", false
	}
	return strings.TrimPrefix(importPath, modulePrefix+"/"), true
}

func ExternalImportViolations(g Graph) []string {
	var out []string
	for pkg, imports := range g {
		from, ok := stripModule(pkg)
		if !ok {
			continue
		}
		for _, imp := range imports {
			if awsIn(from, "internal/domain", "internal/rules", "internal/evaluate", "internal/store", "internal/queue", "internal/submit", "internal/report", "internal/processjob") &&
				(strings.HasPrefix(imp, "github.com/aws/") || strings.HasPrefix(imp, "github.com/aws/aws-cdk-go")) {
				out = append(out, from+" -> "+imp+" (pure packages import AWS)")
			}
		}
	}
	return slices.Sorted(slices.Values(out))
}

func awsIn(from string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if from == prefix || strings.HasPrefix(from, prefix+"/") {
			return true
		}
	}
	return false
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

func TestFixtureAllowedEdgesStayClean(t *testing.T) {
	fixture := Graph{
		modulePrefix + "/cmd/http":                 {modulePrefix + "/internal/adapter/httpapi", modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/submit"},
		modulePrefix + "/cmd/worker":               {modulePrefix + "/internal/adapter/sqs", modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/processjob"},
		modulePrefix + "/internal/adapter/httpapi": {modulePrefix + "/internal/evaluate", modulePrefix + "/internal/submit", modulePrefix + "/internal/report"},
		modulePrefix + "/internal/adapter/sqs":     {modulePrefix + "/internal/processjob"},
		modulePrefix + "/internal/submit":          {modulePrefix + "/internal/queue", modulePrefix + "/internal/store"},
		modulePrefix + "/internal/report":          {modulePrefix + "/internal/store"},
		modulePrefix + "/internal/processjob":      {modulePrefix + "/internal/evaluate", modulePrefix + "/internal/queue", modulePrefix + "/internal/store"},
		modulePrefix + "/internal/evaluate":        {modulePrefix + "/internal/rules", modulePrefix + "/internal/store"},
		modulePrefix + "/internal/rules":           {modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/store":           {modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/queue":           {modulePrefix + "/internal/domain"},
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
