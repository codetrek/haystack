package indexer

import "testing"

func TestGetLangFromFilename_JavaScript(t *testing.T) {
	cases := map[string]string{
		"app.js":        "javascript",
		"component.jsx": "javascript",
		"APP.JS":        "javascript",
		"file.JSX":      "javascript",
	}
	for file, want := range cases {
		got := GetLangFromFilename(file)
		if got != want {
			t.Errorf("GetLangFromFilename(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestGetLangFromFilename_TypeScript(t *testing.T) {
	cases := map[string]string{
		"app.ts":        "typescript",
		"component.tsx": "typescript",
		"FILE.TS":       "typescript",
	}
	for file, want := range cases {
		got := GetLangFromFilename(file)
		if got != want {
			t.Errorf("GetLangFromFilename(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestGetLangFromFilename_Python(t *testing.T) {
	for _, file := range []string{"main.py", "script.PY"} {
		got := GetLangFromFilename(file)
		if got != "python" {
			t.Errorf("GetLangFromFilename(%q) = %q, want python", file, got)
		}
	}
}

func TestGetLangFromFilename_Rust(t *testing.T) {
	got := GetLangFromFilename("lib.rs")
	if got != "rust" {
		t.Errorf("GetLangFromFilename(lib.rs) = %q, want rust", got)
	}
}

func TestGetLangFromFilename_Go(t *testing.T) {
	got := GetLangFromFilename("main.go")
	if got != "go" {
		t.Errorf("GetLangFromFilename(main.go) = %q, want go", got)
	}
}

func TestGetLangFromFilename_CPlusPlus(t *testing.T) {
	cppFiles := []string{
		"main.cc", "lib.cpp", "module.cxx",
		"header.h", "header.hh", "header.hxx", "header.hpp",
	}
	for _, file := range cppFiles {
		got := GetLangFromFilename(file)
		if got != "c++" {
			t.Errorf("GetLangFromFilename(%q) = %q, want c++", file, got)
		}
	}
}

func TestGetLangFromFilename_C(t *testing.T) {
	got := GetLangFromFilename("main.c")
	if got != "c" {
		t.Errorf("GetLangFromFilename(main.c) = %q, want c", got)
	}
}

func TestGetLangFromFilename_CSharp(t *testing.T) {
	got := GetLangFromFilename("Program.cs")
	if got != "C#" {
		t.Errorf("GetLangFromFilename(Program.cs) = %q, want C#", got)
	}
}

func TestGetLangFromFilename_Ruby(t *testing.T) {
	got := GetLangFromFilename("app.rb")
	if got != "ruby" {
		t.Errorf("GetLangFromFilename(app.rb) = %q, want ruby", got)
	}
}

func TestGetLangFromFilename_Java(t *testing.T) {
	got := GetLangFromFilename("Main.java")
	if got != "Java" {
		t.Errorf("GetLangFromFilename(Main.java) = %q, want Java", got)
	}
}

func TestGetLangFromFilename_PHP(t *testing.T) {
	got := GetLangFromFilename("index.php")
	if got != "php" {
		t.Errorf("GetLangFromFilename(index.php) = %q, want php", got)
	}
}

func TestGetLangFromFilename_Swift(t *testing.T) {
	got := GetLangFromFilename("App.swift")
	if got != "swift" {
		t.Errorf("GetLangFromFilename(App.swift) = %q, want swift", got)
	}
}

func TestGetLangFromFilename_UnrecognizedExtension(t *testing.T) {
	unknowns := []string{
		"data.csv", "config.yaml", "file.toml", "readme.md",
		"style.css", "Makefile", "Dockerfile",
	}
	for _, file := range unknowns {
		got := GetLangFromFilename(file)
		if got != "" {
			t.Errorf("GetLangFromFilename(%q) = %q, want empty string", file, got)
		}
	}
}

func TestGetLangFromFilename_NoExtension(t *testing.T) {
	got := GetLangFromFilename("Makefile")
	if got != "" {
		t.Errorf("GetLangFromFilename(Makefile) = %q, want empty string", got)
	}
}

func TestGetLangFromFilename_PathWithDirectories(t *testing.T) {
	got := GetLangFromFilename("src/server/main.go")
	if got != "go" {
		t.Errorf("GetLangFromFilename(src/server/main.go) = %q, want go", got)
	}
}

func TestGetLangFromFilename_DotFile(t *testing.T) {
	got := GetLangFromFilename(".gitignore")
	if got != "" {
		t.Errorf("GetLangFromFilename(.gitignore) = %q, want empty string", got)
	}
}

func TestGetLangFromFilename_DoubleDotExtension(t *testing.T) {
	// e.g. "test.spec.ts" - should pick up .ts
	got := GetLangFromFilename("test.spec.ts")
	if got != "typescript" {
		t.Errorf("GetLangFromFilename(test.spec.ts) = %q, want typescript", got)
	}
}
