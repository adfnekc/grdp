package grdp

// This test exists because a password was written to the log.
//
// protocol/nla/ntlm.go had, on every NLA connection,
//
//	glog.Infof("user: %s, passwd:%s", n.user, n.password)
//
// which is a debugging line someone left behind after getting CredSSP working.
// Nothing caught it: not the tests, not go vet, not a reviewer reading the diff
// of a session whose subject was something else. It was found by someone
// auditing this repository for exactly this, and the only reason to believe it
// will not happen again is a check that runs on every build.
//
// The check looks at expressions rather than text, so that a comment describing
// the mistake - which is what the fix left in place - does not trip it, and so
// that the many log lines with the word "secret" in a message string do not
// either. What it looks for is a log call handed a field or variable whose name
// says it holds a credential.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// credentialNames are the words that make a name a credential. Matched on word
// boundaries so that Nothing and passwordless do not count.
var credentialNames = []string{
	"password", "passwd", "pwd", "passphrase", "secret", "token", "credential",
}

// isCredentialName reports whether an identifier name looks like a credential.
func isCredentialName(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range credentialNames {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// loggedExpression returns the name of a credential expression being logged, or
// the empty string. Only direct field access and plain identifiers count: a
// length, a count or a message string is not the material itself.
func loggedExpression(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		if isCredentialName(v.Sel.Name) {
			return v.Sel.Name
		}
	case *ast.Ident:
		if isCredentialName(v.Name) {
			return v.Name
		}
	}
	return ""
}

// isLogCall reports whether a call is one of the logging helpers. The package is
// matched by name so that a move or a rename to another logging package is
// still covered.
func isLogCall(sel *ast.SelectorExpr) bool {
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if !strings.Contains(strings.ToLower(pkg.Name), "log") {
		return false
	}
	switch sel.Sel.Name {
	case "Info", "Infof", "Debug", "Debugf", "Trace", "Tracef",
		"Warn", "Warnf", "Error", "Errorf", "Print", "Printf", "Fatal", "Fatalf":
		return true
	}
	return false
}

func TestNothingLogsACredential(t *testing.T) {
	root := ".."
	fset := token.NewFileSet()
	checked := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This file contains the words by necessity.
		if strings.HasSuffix(path, "sources_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		checked++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !isLogCall(sel) {
				return true
			}
			for _, arg := range call.Args {
				if name := loggedExpression(arg); name != "" {
					t.Errorf("%s:%d logs %q; a credential must not reach the log",
						path, fset.Position(call.Pos()).Line, name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if checked == 0 {
		t.Fatal("no Go files were checked, so this test is not testing anything")
	}
	t.Logf("checked %d Go files for logged credentials", checked)
}
