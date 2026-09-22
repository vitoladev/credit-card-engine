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
	{name: "rules import a use case, store, queue, or adapter",
		from: "internal/rules", to: []string{"internal/evaluate", "internal/processjob", "internal/submit", "internal/report", "internal/recovery", "internal/markfailed", "internal/queue", "internal/store", "internal/adapter"}},
	{name: "store imports rules, use case, queue, or adapter",
		from: "internal/store", to: []string{"internal/rules", "internal/evaluate", "internal/processjob", "internal/submit", "internal/report", "internal/recovery", "internal/markfailed", "internal/queue", "internal/adapter"}},
	{name: "queue imports rules, use case, store, or adapter",
		from: "internal/queue", to: []string{"internal/rules", "internal/evaluate", "internal/processjob", "internal/submit", "internal/report", "internal/recovery", "internal/markfailed", "internal/store", "internal/adapter"}},
	{name: "evaluate imports another use case or adapter",
		from: "internal/evaluate", to: []string{"internal/processjob", "internal/submit", "internal/report", "internal/queue", "internal/adapter"}},
	{name: "submit imports evaluate, rules, report, or adapter",
		from: "internal/submit", to: []string{"internal/evaluate", "internal/rules", "internal/report", "internal/processjob", "internal/adapter"}},
	{name: "report imports evaluate, rules, queue, submit, or adapter",
		from: "internal/report", to: []string{"internal/evaluate", "internal/rules", "internal/queue", "internal/submit", "internal/processjob", "internal/adapter"}},
	{name: "processjob imports rules, submit, report, or adapter",
		from: "internal/processjob", to: []string{"internal/rules", "internal/submit", "internal/report", "internal/adapter"}},
	{name: "recovery imports evaluate, rules, another use case, or adapter",
		from: "internal/recovery", to: []string{"internal/evaluate", "internal/rules", "internal/submit", "internal/report", "internal/processjob", "internal/markfailed", "internal/adapter"}},
	{name: "markfailed imports evaluate, rules, another use case, or adapter",
		from: "internal/markfailed", to: []string{"internal/evaluate", "internal/rules", "internal/submit", "internal/report", "internal/processjob", "internal/recovery", "internal/adapter"}},
	{name: "adapter/httpapi imports processjob, markfailed, or worker adapters",
		from: "internal/adapter/httpapi", to: []string{"internal/processjob", "internal/markfailed", "internal/adapter/sqs", "internal/adapter/dlq", "internal/adapter/ddb", "internal/adapter/sqspub"}},
	{name: "adapter/sqs imports submit, report, recovery, or httpapi",
		from: "internal/adapter/sqs", to: []string{"internal/submit", "internal/report", "internal/recovery", "internal/adapter/httpapi"}},
	{name: "adapter/dlq imports httpapi, submit, report, recovery, or processjob",
		from: "internal/adapter/dlq", to: []string{"internal/adapter/httpapi", "internal/submit", "internal/report", "internal/recovery", "internal/processjob"}},
	{name: "adapter/telemetry imports a use case or sibling adapter",
		from: "internal/adapter/telemetry", to: []string{"internal/evaluate", "internal/processjob", "internal/submit", "internal/report", "internal/recovery", "internal/markfailed", "internal/adapter/httpapi", "internal/adapter/sqs", "internal/adapter/dlq", "internal/adapter/ddb", "internal/adapter/sqspub"}},
	{name: "cmd/http imports processjob, markfailed, or the queue consumers",
		from: "cmd/http", to: []string{"internal/processjob", "internal/markfailed", "internal/adapter/sqs", "internal/adapter/dlq"}},
	{name: "cmd/worker imports submit, report, recovery, or httpapi",
		from: "cmd/worker", to: []string{"internal/submit", "internal/report", "internal/recovery", "internal/adapter/httpapi", "internal/adapter/sqspub"}},
	{name: "cmd/dlq imports submit, report, recovery, processjob, or httpapi",
		from: "cmd/dlq", to: []string{"internal/submit", "internal/report", "internal/recovery", "internal/processjob", "internal/adapter/httpapi", "internal/adapter/sqspub"}},
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
			if underAny(from, "internal/domain", "internal/rules", "internal/evaluate", "internal/store", "internal/queue", "internal/submit", "internal/report", "internal/processjob", "internal/recovery", "internal/markfailed") &&
				strings.HasPrefix(imp, "github.com/aws/") {
				out = append(out, from+" -> "+imp+" (pure packages import AWS)")
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

func TestFixtureDLQAdapterImportingHTTPAPIFails(t *testing.T) {
	fixture := Graph{
		modulePrefix + "/internal/adapter/dlq": {modulePrefix + "/internal/adapter/httpapi", modulePrefix + "/internal/submit", modulePrefix + "/internal/report"},
	}
	if got := len(Violations(fixture)); got != 3 {
		t.Fatalf("expected 3 violations for dlq -> httpapi, submit, report, got %d", got)
	}
}

func TestFixtureAllowedEdgesStayClean(t *testing.T) {
	fixture := Graph{
		modulePrefix + "/cmd/http":                   {modulePrefix + "/internal/adapter/httpapi", modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/adapter/telemetry", modulePrefix + "/internal/submit"},
		modulePrefix + "/cmd/worker":                 {modulePrefix + "/internal/adapter/sqs", modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/processjob"},
		modulePrefix + "/cmd/dlq":                    {modulePrefix + "/internal/adapter/dlq", modulePrefix + "/internal/adapter/ddb", modulePrefix + "/internal/adapter/telemetry", modulePrefix + "/internal/markfailed"},
		modulePrefix + "/internal/adapter/dlq":       {modulePrefix + "/internal/markfailed"},
		modulePrefix + "/internal/adapter/telemetry": {modulePrefix + "/internal/store", modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/recovery":          {modulePrefix + "/internal/queue", modulePrefix + "/internal/store"},
		modulePrefix + "/internal/markfailed":        {modulePrefix + "/internal/queue", modulePrefix + "/internal/store"},
		modulePrefix + "/internal/adapter/httpapi":   {modulePrefix + "/internal/evaluate", modulePrefix + "/internal/submit", modulePrefix + "/internal/report", modulePrefix + "/internal/recovery", modulePrefix + "/internal/adapter/telemetry"},
		modulePrefix + "/internal/adapter/sqs":       {modulePrefix + "/internal/processjob", modulePrefix + "/internal/adapter/telemetry"},
		modulePrefix + "/internal/submit":            {modulePrefix + "/internal/queue", modulePrefix + "/internal/store"},
		modulePrefix + "/internal/report":            {modulePrefix + "/internal/store"},
		modulePrefix + "/internal/processjob":        {modulePrefix + "/internal/evaluate", modulePrefix + "/internal/queue", modulePrefix + "/internal/store"},
		modulePrefix + "/internal/evaluate":          {modulePrefix + "/internal/rules", modulePrefix + "/internal/store"},
		modulePrefix + "/internal/rules":             {modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/store":             {modulePrefix + "/internal/domain"},
		modulePrefix + "/internal/queue":             {modulePrefix + "/internal/domain"},
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
