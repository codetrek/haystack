package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMd5Hash(t *testing.T) {
	hash := Md5Hash([]byte("hello"))
	assert.Equal(t, "5d41402abc4b2a76b9719d911017c592", hash)
}

func TestMd5Hash_Empty(t *testing.T) {
	hash := Md5Hash([]byte(""))
	assert.Equal(t, "d41d8cd98f00b204e9800998ecf8427e", hash)
}

func TestMd5HashString(t *testing.T) {
	hash := Md5HashString("hello")
	assert.Equal(t, "5d41402abc4b2a76b9719d911017c592", hash)
}

func TestNormalizePath_Empty(t *testing.T) {
	assert.Equal(t, "", NormalizePath(""))
}

func TestNormalizePath_Relative(t *testing.T) {
	result := NormalizePath("foo/bar")
	assert.Equal(t, "foo/bar", result)
}

func TestNormalizePath_Absolute(t *testing.T) {
	result := NormalizePath("/usr/local/bin")
	assert.Equal(t, "/usr/local/bin", result)
}

func TestNormalizePath_CleansDots(t *testing.T) {
	result := NormalizePath("/usr/local/../bin")
	assert.Equal(t, "/usr/bin", result)
}

func TestNormalizePath_CleansSlashes(t *testing.T) {
	result := NormalizePath("/usr//local///bin")
	assert.Equal(t, "/usr/local/bin", result)
}
