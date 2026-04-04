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

func TestGetContentHash(t *testing.T) {
	hash1 := GetContentHash([]byte("hello"))
	hash2 := GetContentHash([]byte("hello"))
	hash3 := GetContentHash([]byte("world"))

	assert.Equal(t, hash1, hash2)
	assert.NotEqual(t, hash1, hash3)
	assert.Len(t, hash1, 32) // MD5 hex is 32 chars
}

func TestIsLikelyText(t *testing.T) {
	// Text content
	assert.True(t, IsLikelyText([]byte("package main\n\nfunc main() {}\n")))
	assert.True(t, IsLikelyText([]byte("Hello, World!\nThis is a text file.")))
	assert.True(t, IsLikelyText([]byte(`{"key": "value"}`)))

	// Binary content
	binaryData := make([]byte, 100)
	for i := range binaryData {
		binaryData[i] = byte(i % 32) // lots of control chars
	}
	assert.False(t, IsLikelyText(binaryData))
}

func TestIsTextMIME(t *testing.T) {
	assert.True(t, isTextMIME("text/plain"))
	assert.True(t, isTextMIME("text/html"))
	assert.True(t, isTextMIME("application/json"))
	assert.True(t, isTextMIME("application/xml"))
	assert.True(t, isTextMIME("application/javascript"))
	assert.True(t, isTextMIME("application/vnd.api+json"))
	assert.True(t, isTextMIME("application/svg+xml"))

	assert.False(t, isTextMIME("application/octet-stream"))
	assert.False(t, isTextMIME("image/png"))
}

func TestIsMediaMIME(t *testing.T) {
	assert.True(t, isMediaMIME("image/png"))
	assert.True(t, isMediaMIME("video/mp4"))
	assert.True(t, isMediaMIME("audio/mpeg"))

	assert.False(t, isMediaMIME("text/plain"))
	assert.False(t, isMediaMIME("application/json"))
}

func TestIsProbablyText(t *testing.T) {
	// All printable
	assert.True(t, isProbablyText([]byte("Hello World\n")))

	// Mostly binary
	data := make([]byte, 100)
	for i := range data {
		data[i] = 0x01
	}
	assert.False(t, isProbablyText(data))

	// Unicode text (high bytes)
	assert.True(t, isProbablyText([]byte("中文内容测试\n")))
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
