package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNotIndexiable(t *testing.T) {
	assert.True(t, IsNotIndexiable("file.exe"))
	assert.True(t, IsNotIndexiable("file.png"))
	assert.True(t, IsNotIndexiable("file.jpg"))
	assert.True(t, IsNotIndexiable("file.zip"))
	assert.True(t, IsNotIndexiable("file.pdf"))
	assert.True(t, IsNotIndexiable("file.mp4"))
	assert.True(t, IsNotIndexiable("file.dll"))
	assert.True(t, IsNotIndexiable("FILE.EXE")) // case insensitive

	assert.False(t, IsNotIndexiable("file.go"))
	assert.False(t, IsNotIndexiable("file.js"))
	assert.False(t, IsNotIndexiable("file.py"))
	assert.False(t, IsNotIndexiable("file.md"))
	assert.False(t, IsNotIndexiable("Makefile"))
	assert.False(t, IsNotIndexiable("file.yaml"))
}

func TestGetLangFromFilename(t *testing.T) {
	assert.Equal(t, "go", GetLangFromFilename("main.go"))
	assert.Equal(t, "javascript", GetLangFromFilename("app.js"))
	assert.Equal(t, "javascript", GetLangFromFilename("app.jsx"))
	assert.Equal(t, "typescript", GetLangFromFilename("app.ts"))
	assert.Equal(t, "typescript", GetLangFromFilename("app.tsx"))
	assert.Equal(t, "python", GetLangFromFilename("script.py"))
	assert.Equal(t, "rust", GetLangFromFilename("lib.rs"))
	assert.Equal(t, "c++", GetLangFromFilename("main.cpp"))
	assert.Equal(t, "c++", GetLangFromFilename("header.h"))
	assert.Equal(t, "c", GetLangFromFilename("main.c"))
	assert.Equal(t, "C#", GetLangFromFilename("Program.cs"))
	assert.Equal(t, "ruby", GetLangFromFilename("app.rb"))
	assert.Equal(t, "Java", GetLangFromFilename("Main.java"))
	assert.Equal(t, "php", GetLangFromFilename("index.php"))
	assert.Equal(t, "swift", GetLangFromFilename("app.swift"))
	assert.Equal(t, "", GetLangFromFilename("README.md"))
	assert.Equal(t, "", GetLangFromFilename("config.yaml"))
}
