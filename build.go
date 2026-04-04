//go:build ignore

package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Target struct {
	GOOS   string
	GOARCH string
	Ext    string
}

var targets = []Target{
	{"windows", "amd64", ".exe"},
	{"windows", "arm64", ".exe"},
	{"linux", "amd64", ""},
	{"linux", "arm64", ""},
	{"darwin", "amd64", ""},
	{"darwin", "arm64", ""},
}

func main() {
	appName := "haystack"
	outputDir := "dist"
	version := getVersion()

	os.RemoveAll(outputDir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		panic(err)
	}

	for _, t := range targets {
		fmt.Printf("🔨 Building for %s/%s...\n", t.GOOS, t.GOARCH)

		binName := fmt.Sprintf("%s%s", appName, t.Ext)
		binPath := filepath.Join(outputDir, binName)

		ldflags := fmt.Sprintf("-s -w -X 'main.version=%s'", version)
		args := []string{
			"build",
			"-trimpath",
			"-ldflags", ldflags,
			"-gcflags=all=-l",
			"-o", binPath,
			"./cmd/haystack/",
		}

		cmd := exec.Command("go", args...)
		cmd.Env = append(os.Environ(),
			"GOOS="+t.GOOS,
			"GOARCH="+t.GOARCH,
			"CGO_ENABLED=0",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Build failed: %v\n", err)
			continue
		}

		pwd, _ := os.Getwd()
		deps := filepath.Join(pwd, "deps", fmt.Sprintf("%s-%s", t.GOOS, t.GOARCH))

		zipName := fmt.Sprintf("%s-%s-%s-v%s.zip", appName, t.GOOS, t.GOARCH, version)
		zipPath := filepath.Join(outputDir, zipName)

		if err := zipFile(zipPath, binPath, deps); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Zip failed: %v\n", err)
		} else {
			fmt.Printf("✅ Built and zipped: %s\n", zipName)
		}

		_ = os.Remove(binPath)
	}

	os.WriteFile(filepath.Join(outputDir, "VERSION"), []byte(version), 0644)
}

func getVersion() string {
	data, err := os.ReadFile("VERSION")
	if err != nil {
		fmt.Println("❌ Failed to read VERSION file:", err)
		os.Exit(1)
	}
	return strings.TrimSpace(string(data))
}

func zipFile(zipPath, filePath string, depsDir string) error {
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	w := zip.NewWriter(zipFile)
	defer w.Close()

	// Add the main binary file
	fileToZip, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fileToZip.Close()

	info, err := fileToZip.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.Base(filePath)
	header.Method = zip.Deflate

	writer, err := w.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, fileToZip)
	if err != nil {
		return err
	}

	// Add all files from the deps directory if it exists
	if _, err := os.Stat(depsDir); !os.IsNotExist(err) {
		err = filepath.Walk(depsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Calculate relative path for the zip file entry
			relPath, err := filepath.Rel(depsDir, path)
			if err != nil {
				return err
			}

			// For directories, create a directory entry in the zip
			if info.IsDir() {
				// Ensure directory path ends with separator to be recognized as a directory
				if !strings.HasSuffix(relPath, string(os.PathSeparator)) {
					relPath += string(os.PathSeparator)
				}

				zipHeader, err := zip.FileInfoHeader(info)
				if err != nil {
					return err
				}
				zipHeader.Name = relPath
				zipHeader.Method = zip.Deflate
				fmt.Printf("Adding directory %s\n", path)

				_, err = w.CreateHeader(zipHeader)
				if err != nil {
					return err
				}
				return nil
			}

			// Open the file
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			// Create zip header
			zipHeader, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			zipHeader.Name = relPath
			zipHeader.Method = zip.Deflate
			fmt.Printf("Adding %s\n", path)

			// Create writer and copy file contents
			zipWriter, err := w.CreateHeader(zipHeader)
			if err != nil {
				return err
			}

			_, err = io.Copy(zipWriter, file)
			return err
		})

		if err != nil {
			return err
		}
	}

	return nil
}
