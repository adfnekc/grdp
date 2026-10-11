// Command docaudit lists exported identifiers without doc comments.
//
// It exists because the question "is the API documented" was being answered by
// reading, which is how it went unfinished twice. Grouped declarations count as
// documented when the block has a comment, since a run of wire constants that
// mirrors a specification section is meant to be explained once, not per line.
package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// inBuild reports whether a file is part of the build for the current context,
// which is what decides whether godoc renders the identifiers in it at all.
//
// It matters here because the alternative is counting declarations in a file
// excluded by a build tag: this repository has one, a Windows-only port that is
// switched off with //go:build ignore, and its eleven identifiers were being
// reported as documentation debt for code that is not compiled.
func inBuild(dir, name string, ctx *build.Context) bool {
	if strings.HasSuffix(name, "_test.go") {
		return false
	}
	ok, err := ctx.MatchFile(dir, name)
	return err == nil && ok
}

func main() {
	counts := map[string]int{}
	for _, dir := range os.Args[1:] {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() || strings.HasPrefix(filepath.Base(path), ".") {
				return nil
			}
			fset := token.NewFileSet()
			ctx := build.Default
			pkgs, err := parser.ParseDir(fset, path, func(fi os.FileInfo) bool {
				return inBuild(path, fi.Name(), &ctx)
			}, parser.ParseComments)
			if err != nil || len(pkgs) == 0 {
				return nil
			}
			for name, pkg := range pkgs {
				d := doc.New(pkg, name, 0)
				missing := []string{}
				add := func(kind, n string, hasDoc bool) {
					if !hasDoc && n != "" && ast.IsExported(n) {
						missing = append(missing, kind+" "+n)
					}
				}
				for _, t := range d.Types {
					add("type", t.Name, t.Doc != "")
					for _, v := range t.Vars {
						for _, n := range v.Names {
							add("field", t.Name+"."+n, v.Doc != "")
						}
					}
					for _, f := range t.Funcs {
						add("func", t.Name+"."+f.Name, f.Doc != "")
					}
					for _, m := range t.Methods {
						add("method", t.Name+"."+m.Name, m.Doc != "")
					}
					for _, c := range t.Consts {
						add("const", t.Name+"."+strings.Join(c.Names, ","), c.Doc != "")
					}
				}
				for _, f := range d.Funcs {
					add("func", f.Name, f.Doc != "")
				}
				for _, v := range d.Vars {
					add("var", strings.Join(v.Names, ","), v.Doc != "")
				}
				for _, c := range d.Consts {
					add("const", strings.Join(c.Names, ","), c.Doc != "")
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					rel := strings.TrimPrefix(path, "./")
					counts[rel] = len(missing)
					fmt.Printf("%s: %d undocumented\n", rel, len(missing))
					for _, m := range missing {
						fmt.Printf("    %s\n", m)
					}
				}
			}
			return nil
		})
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	fmt.Printf("\ntotal undocumented: %d across %d packages\n", total, len(counts))
}
