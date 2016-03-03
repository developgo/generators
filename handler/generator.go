// Handler builds typed golang http handlers.
//
// Given a func F :
//  func F(x X) (status int, resp interface{}) )
// and an encoding pkg like encoding/json.
//
// handler will create an http handler :
//
//   func FHandlerFORMAT(w http.ResponseWriter, r *http.Request)
//   // decode
//   // call F()
//
// The file is created in the same package and directory as the package that defines F.
//
//ex:
//  //go:generate handler -func=PutJob -encoding encoding/json
//  package jober
//
//  type job struct { A string }
//
//  func PutJob(j job) (int, interface{}) {
//    return nil, 200
//  }
//
// running
//
//  go generate pkg.go/foo/jober
//
// will create generated_handlers.go:
//  import "encoding/json"
//
//  func PutJobHandlerJSON(w http.ResponseWriter, r *http.Request) {
//      x := job{}
//      err := json.NewDecoder(r.Body).Decode(&x)
//      if err != nil {
//          w.WriteHeader(http.StatusBadRequest)
//          return
//      }
//      s, resp := PutJob(x)
//      w.WriteHeader(s)
//      json.NewEncoder(w).Encode(resp)
//  }
//
// so now you can just worry about what PutJob does.
//
// pkg existence will be checked.
// The pkg needs to have funcs :
//  func NewDecoder(r io.Reader) *Decoder
//  func NewEncoder(r io.Reader) *Encoder
// and types
//  type Encoder interface {
//      Encode(v interface{}) error
//  }
//  type Decoder interface {
//      Decode(v interface{}) error
//  }
//
//
// Typically this process would be run using go generate, by writing:
//
//  //go:generate handler -encoding encoding/json -func PutJob
//
// at the beginning of your .go file
//
//
// The -encoding and the -func flags accepts a comma-separated list of strings.
// So you can have n handler working in m encoding
//
// Name of the created file can be overridden
// with the -output flag.
//
// Support of contexts is comming soon.
package main // import "github.com/azr/handler"

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"golang.org/x/tools/go/types"

	_ "golang.org/x/tools/go/gcimporter"
)

var (
	funcNames        = flag.String("func", "", "comma-separated list of func names; must be set")
	encodingPkgNames = flag.String("encoding", "", "comma-separated list of encoding pkgs; must be set")
	output           = flag.String("output", "", "output file name; default srcdir/generated_handlers.go")
)

// Usage is a replacement usage function for the flags package.
func Usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\thandler [flags] -func F -encoding 'encoding/json' [directory]\n")
	fmt.Fprintf(os.Stderr, "\thandler [flags] -func F -encoding 'encoding/json' files... # Must be a single package\n")
	fmt.Fprintf(os.Stderr, "For more information, see:\n")
	fmt.Fprintf(os.Stderr, "\thttp://godoc.org/github.com/azr/handler\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	flag.PrintDefaults()
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("handler: ")
	flag.Usage = Usage
	flag.Parse()
	if len(*funcNames) == 0 || len(*encodingPkgNames) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	funcs := strings.Split(*funcNames, ",")
	encodings := strings.Split(*encodingPkgNames, ",")

	// We accept either one directory or a list of files. Which do we have?
	args := flag.Args()
	if len(args) == 0 {
		// Default: process whole package in current directory.
		args = []string{"."}
	}

	// Parse the package once.
	var (
		dir string
		g   Generator
	)
	if len(args) == 1 && isDirectory(args[0]) {
		dir = args[0]
		g.parsePackageDir(args[0])
	} else {
		dir = filepath.Dir(args[0])
		g.parsePackageFiles(args)
	}

	// Print the header and package clause.
	g.Printf("// Code generated by \"handler %s\"; DO NOT EDIT\n", strings.Join(os.Args[1:], " "))
	g.Printf("\n")
	g.Printf("package %s\n", g.pkg.name)
	g.Printf("\n")

	for _, encodingPkgName := range encodings { // check that encoding pkgs exist
		_, err := build.Import(encodingPkgName, ".", 0)
		if err != nil {
			log.Fatalf("cannot use pkg %s: %s", encodingPkgName, err)
			return
		}
		g.Printf("import \"%s\"\n", encodingPkgName)
	}

	// Run generate for each type.
	for _, funcName := range funcs {
		for _, encodingPkgName := range encodings {
			pkg, _ := build.Import(encodingPkgName, ".", 0)
			g.generate(funcName, pkg.Name)
		}
	}

	// Format the output.
	src := g.format()

	// Write to file.
	outputName := *output
	if outputName == "" {
		outputName = filepath.Join(dir, "generated_handlers.go")
	}
	err := ioutil.WriteFile(outputName, src, 0644)
	if err != nil {
		log.Fatalf("writing output: %s", err)
	}
}

// isDirectory reports whether the named file is a directory.
func isDirectory(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		log.Fatal(err)
	}
	return info.IsDir()
}

// Generator holds the state of the analysis. Primarily used to buffer
// the output for format.Source.
type Generator struct {
	buf bytes.Buffer // Accumulated output.
	pkg *Package     // Package we are scanning.
}

func (g *Generator) Printf(format string, args ...interface{}) {
	fmt.Fprintf(&g.buf, format, args...)
}

// File holds a single parsed file and associated data.
type File struct {
	pkg  *Package  // Package to which this file belongs.
	file *ast.File // Parsed AST.
	// These fields are reset for each type being generated.
	funcName, encodingPkgName string // Name of the type.
	paramfullname             string
	found                     bool
}

type Package struct {
	dir      string
	name     string
	defs     map[*ast.Ident]types.Object
	files    []*File
	typesPkg *types.Package
}

// parsePackageDir parses the package residing in the directory.
func (g *Generator) parsePackageDir(directory string) {
	pkg, err := build.Default.ImportDir(directory, 0)
	if err != nil {
		log.Fatalf("cannot process directory %s: %s", directory, err)
	}
	var names []string
	names = append(names, pkg.GoFiles...)
	names = append(names, pkg.CgoFiles...)
	// TODO: Need to think about constants in test files. Maybe write type_string_test.go
	// in a separate pass? For later.
	// names = append(names, pkg.TestGoFiles...) // These are also in the "foo" package.
	names = append(names, pkg.SFiles...)
	names = prefixDirectory(directory, names)
	g.parsePackage(directory, names, nil)
}

// parsePackageFiles parses the package occupying the named files.
func (g *Generator) parsePackageFiles(names []string) {
	g.parsePackage(".", names, nil)
}

// prefixDirectory places the directory name on the beginning of each name in the list.
func prefixDirectory(directory string, names []string) []string {
	if directory == "." {
		return names
	}
	ret := make([]string, len(names))
	for i, name := range names {
		ret[i] = filepath.Join(directory, name)
	}
	return ret
}

// parsePackage analyzes the single package constructed from the named files.
// If text is non-nil, it is a string to be used instead of the content of the file,
// to be used for testing. parsePackage exits if there is an error.
func (g *Generator) parsePackage(directory string, names []string, text interface{}) {
	var files []*File
	var astFiles []*ast.File
	g.pkg = new(Package)
	fs := token.NewFileSet()
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		parsedFile, err := parser.ParseFile(fs, name, text, 0)
		if err != nil {
			log.Fatalf("parsing package: %s: %s", name, err)
		}
		astFiles = append(astFiles, parsedFile)
		files = append(files, &File{
			file: parsedFile,
			pkg:  g.pkg,
		})
	}
	if len(astFiles) == 0 {
		log.Fatalf("%s: no buildable Go files", directory)
	}
	g.pkg.name = astFiles[0].Name.Name
	g.pkg.files = files
	g.pkg.dir = directory
	// Type check the package.
	g.pkg.check(fs, astFiles)
}

// check type-checks the package. The package must be OK to proceed.
func (pkg *Package) check(fs *token.FileSet, astFiles []*ast.File) {
	pkg.defs = make(map[*ast.Ident]types.Object)
	config := types.Config{FakeImportC: true}
	info := &types.Info{
		Defs: pkg.defs,
	}
	typesPkg, err := config.Check(pkg.dir, fs, astFiles, info)
	if err != nil {
		log.Fatalf("checking package: %s", err)
	}
	pkg.typesPkg = typesPkg
}

// generate produces the Http handler method for the func and encoding
func (g *Generator) generate(funcName, encodingPkgName string) {
	found := false
	paramfullname := ""
	for _, file := range g.pkg.files {
		// Set the state for this run of the walker.
		file.funcName = funcName
		if file.file != nil {
			ast.Inspect(file.file, file.genDecl)
			if file.found {
				found = true
				paramfullname = file.paramfullname
			}
		}
	}

	if found {
		g.build(funcName, encodingPkgName, paramfullname)
	} else {
		fmt.Printf("Func not found: %s", funcName)
	}
}

// format returns the gofmt-ed contents of the Generator's buffer.
func (g *Generator) format() []byte {
	src, err := format.Source(g.buf.Bytes())
	if err != nil {
		// Should never happen, but can arise when developing this code.
		// The user can compile the output to see the error.
		log.Printf("warning: internal error: invalid Go generated: %s", err)
		log.Printf("warning: compile the package to analyze the error")
		return g.buf.Bytes()
	}
	return src
}

// genDecl processes one declaration clause.
func (f *File) genDecl(node ast.Node) bool {
	decl, ok := node.(*ast.FuncDecl)
	if !ok {
		// We only care about func declarations.
		return true
	}
	if decl.Name.Name == f.funcName {
		if len(decl.Type.Params.List) != 1 {
			log.Printf("%s should take only one parameter, found %d instead", f.funcName, len(decl.Type.Params.List))
			return false
		}

		switch v := decl.Type.Params.List[0].Type.(type) { // get var type
		case *ast.Ident:
			// plain type like from type x struct {}
			f.paramfullname = v.Name
		case *ast.SelectorExpr:
			// import type like pkgname.X
			f.paramfullname = fmt.Sprintf("%s.%s", v.X, v.Sel)
		default:
			log.Printf("Could not guess var full name, type not expected: %v", v)
			return false
		}
		f.found = true
	}
	return false
}

// build generates the variables and String method for a single run of contiguous values.
func (g *Generator) build(funcName, pkgName, paramfullname string) {
	type Handler struct {
		Func        string
		EncodingPkg string
		T           string
	}

	funcMap := template.FuncMap{
		"ToUpper": strings.ToUpper,
	}

	t := template.Must(template.New("handler").Funcs(funcMap).Parse(handlerWrap))

	err := t.Execute(&g.buf, Handler{
		Func:        funcName,
		EncodingPkg: pkgName,
		T:           paramfullname,
	})
	checkError(err)
}

const handlerWrap = `
func {{.Func}}Handler{{.EncodingPkg | ToUpper}}(w http.ResponseWriter, r *http.Request) {
	x := {{.T}}{}
	err := {{.EncodingPkg}}.NewDecoder(r.Body).Decode(&x)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	resp, s := {{.Func}}(x)
	w.WriteHeader(s)
	{{.EncodingPkg}}.NewEncoder(w).Encode(resp)
}
`

func checkError(err error) {
	if err != nil {
		fmt.Println("Fatal error ", err.Error())
		os.Exit(1)
	}
}
