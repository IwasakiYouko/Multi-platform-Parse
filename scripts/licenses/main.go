// Command licenses bundles dependency license texts for binary redistribution.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type module struct {
	Path, Version, Dir string
	Main               bool
	Replace            *module
}

func main() {
	out := flag.String("out", "", "required output directory")
	flag.Parse()
	if *out == "" {
		fail(fmt.Errorf("-out is required"))
	}
	data, err := exec.Command("go", "list", "-m", "-json", "all").Output()
	if err != nil {
		fail(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var m module
		if err := decoder.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			fail(err)
		}
		if m.Main {
			continue // The project does not declare a license for its own code.
		}
		if m.Replace != nil {
			fail(fmt.Errorf("unsupported module replacement for %s; preserve explicit attribution before distributing", m.Path))
		}
		if m.Dir == "" {
			fail(fmt.Errorf("module %s@%s is not downloaded", m.Path, m.Version))
		}
		licensed := false
		err := filepath.WalkDir(m.Dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			name := strings.ToUpper(entry.Name())
			for _, prefix := range []string{"LICENSE", "LICENCE", "UNLICENSE", "COPYING", "NOTICE", "COPYRIGHT", "AUTHORS", "PATENTS"} {
				if !strings.HasPrefix(name, prefix) {
					continue
				}
				relative, err := filepath.Rel(m.Dir, path)
				if err != nil {
					return err
				}
				if err := copyFile(path, filepath.Join(*out, "modules", m.Path+"@"+m.Version, relative)); err != nil {
					return err
				}
				if prefix == "LICENSE" || prefix == "LICENCE" || prefix == "UNLICENSE" || prefix == "COPYING" {
					licensed = true
				}
				break
			}
			return nil
		})
		if err != nil {
			fail(err)
		}
		if !licensed {
			fail(fmt.Errorf("no license text found for %s@%s", m.Path, m.Version))
		}
	}
	for _, name := range []string{"LICENSE", "PATENTS"} {
		if err := copyFile(filepath.Join(runtime.GOROOT(), name), filepath.Join(*out, "go", name)); err != nil {
			fail(err)
		}
	}
	if err := os.WriteFile(filepath.Join(*out, "go", "VERSION"), []byte(runtime.Version()+"\n"), 0o644); err != nil {
		fail(err)
	}
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty attribution file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
