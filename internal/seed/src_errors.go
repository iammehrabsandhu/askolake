package seed

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

// ErrorMessages extracts fmt.Errorf / errors.New messages from Go sources
// and json struct tags (returned as identifiers).
func ErrorMessages(co Checkout) ([]domain.Chunk, []string, error) {
	var files []string
	err := filepath.WalkDir(co.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("errors %s: %w", co.Repo.Name, err)
	}

	type key struct{ msg, fn string }
	seen := make(map[key]bool)
	var chunks []domain.Chunk
	idents := make(map[string]struct{})
	parseFailures := 0
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			parseFailures++
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			parseFailures++
			continue
		}
		rel, _ := filepath.Rel(co.Dir, path)
		rel = filepath.ToSlash(rel)
		lines := strings.Split(string(src), "\n")

		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.StructType:
				for _, fld := range x.Fields.List {
					if name := jsonTagName(fld.Tag); name != "" {
						idents[name] = struct{}{}
					}
				}
			case *ast.CallExpr:
				msg, ok := errorCallMessage(x)
				if !ok {
					return true
				}
				pieces := utils.ErrorPieces(msg)
				if len(utils.Longest(pieces)) < tuning.ErrLiteralMinLen {
					return true
				}
				pattern := utils.ErrorPattern(pieces)
				if _, err := regexp.Compile(pattern); err != nil {
					return true
				}
				fn := enclosingFunc(f, x.Pos())
				if seen[key{msg, fn}] {
					return true
				}
				seen[key{msg, fn}] = true
				line := fset.Position(x.Pos()).Line
				lo := max(1, line-tuning.ErrContextBefore)
				hi := min(len(lines), line+tuning.ErrContextAfter)
				snippet := strings.Join(lines[lo-1:hi], "\n")
				url := fmt.Sprintf("%s#L%d", BlobURL(co.Repo.URL, co.SHA, rel), line)
				chunks = append(chunks, domain.Chunk{
					URL:   url,
					Page:  utils.StripFragment(url),
					Title: co.Repo.Name + " error: " + msg,
					Text: fmt.Sprintf("Error message: %s\nLocation: %s/%s:%d in func %s\n```go\n%s\n```",
						msg, co.Repo.Name, rel, line, fn, snippet),
					Source:     domain.SourceCode,
					Product:    co.Repo.Product,
					ErrPattern: pattern,
				})
			}
			return true
		})
	}
	if parseFailures > 0 {
		slog.Warn("errors: skipped unparsable Go files", "repo", co.Repo.Name, "count", parseFailures)
	}
	out := make([]string, 0, len(idents))
	for k := range idents {
		out = append(out, k)
	}
	return chunks, out, nil
}

// errorCallMessage returns the literal message of fmt.Errorf("...") or errors.New("...").
func errorCallMessage(c *ast.CallExpr) (string, bool) {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || len(c.Args) == 0 {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || !((pkg.Name == "fmt" && sel.Sel.Name == "Errorf") || (pkg.Name == "errors" && sel.Sel.Name == "New")) {
		return "", false
	}
	lit, ok := c.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	msg, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return msg, true
}

func enclosingFunc(f *ast.File, pos token.Pos) string {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || pos < fd.Pos() || pos > fd.End() {
			continue
		}
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			return recvName(fd.Recv.List[0].Type) + "." + fd.Name.Name
		}
		return fd.Name.Name
	}
	return "(package scope)"
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

func jsonTagName(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	raw, err := strconv.Unquote(tag.Value)
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(reflect.StructTag(raw).Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}
