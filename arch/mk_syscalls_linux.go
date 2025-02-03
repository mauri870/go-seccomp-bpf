// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

//go:build ignore
// +build ignore

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

const (
	baseURL = "https://raw.githubusercontent.com/torvalds/linux/"
)

// TemplateParams is the data used in evaluating the template.
type TemplateParams struct {
	LinuxVersion string
	Arches       []Arch
}

// Arch contains all the syscalls for a single architecture.
type Arch struct {
	Name     string
	Syscalls []*Syscall
}

// Syscall represents a single system call.
type Syscall struct {
	Num  int
	Name string
}

const fileTemplate = `// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// Code generated by mk_syscalls_linux.go - DO NOT EDIT.

package arch

// Based on Linux {{ .LinuxVersion }}.
{{ range $arch := .Arches }}
var syscalls{{ $arch.Name }} = map[int]string{
{{- range $s := $arch.Syscalls }}
	{{ $s.Num }}: "{{ $s.Name }}",
{{- end }}
}
{{ end }}
`

var tmpl = template.Must(template.New("syscalls").Parse(fileTemplate))

type builderFunc func(dir string) (*Arch, error)

func buildARM(dir string) (*Arch, error) {
	const (
		tablePath  = "/arch/arm/tools/syscall.tbl"
		headerPath = "/arch/arm/include/uapi/asm/unistd.h" // ARM private syscalls.
		armNrBase  = 0x0f0000                              // Base value for ARM private syscalls.
	)

	syscallsA, err := readSyscalls(tablePath, dir, func(line string) (*Syscall, error) {
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			return nil, nil
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("unexpected line format: %v", line)
		}

		// Filter out ARM OABI. http://wiki.embeddedarm.com/wiki/EABI_vs_OABI
		if fields[1] == "oabi" {
			return nil, nil
		}

		num, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse syscall number: %v at '%v'", err, line)
		}

		return &Syscall{
			Num:  num,
			Name: fields[2],
		}, nil
	})
	if err != nil {
		return nil, err
	}

	armUnistdSycallRegex := regexp.MustCompile(
		`^#define __ARM_NR_(?P<syscall>[a-z0-9_]+)\s+\(__ARM_NR_BASE\+(?P<number>\d+)\)`)

	syscallsB, err := readSyscalls(headerPath, dir, func(line string) (*Syscall, error) {
		matches := armUnistdSycallRegex.FindStringSubmatch(line)
		if len(matches) != 3 {
			return nil, nil
		}

		num, err := strconv.Atoi(matches[2])
		if err != nil {
			return nil, fmt.Errorf("failed to parse syscall number: %v at '%v'", err, line)
		}
		num += armNrBase

		return &Syscall{
			Num:  num,
			Name: matches[1],
		}, nil
	})
	if err != nil {
		return nil, err
	}

	syscalls := append(syscallsA, syscallsB...)

	sort.Slice(syscalls, func(i, j int) bool {
		return syscalls[i].Num < syscalls[j].Num
	})

	return &Arch{
		Name:     "ARM",
		Syscalls: syscalls,
	}, nil
}

func buildAARCH64(dir string) (*Arch, error) {
	const (
		headerPath = "/include/uapi/asm-generic/unistd.h"
		sentinel   = "syscalls"
	)

	omit := map[string]bool{
		// sync_file_range2 shares a syscall number with
		// sync_file_range guarded by __ARCH_WANT_SYNC_FILE_RANGE2.
		// It is not possible to generate a map with both.
		"sync_file_range2": true,
	}

	armUnistdSycallRegex := regexp.MustCompile(
		`^#define __NR(?:3264)?_(?P<syscall>[a-z0-9_]+)\s+(?P<number>\d+)`)

	syscalls, err := readSyscalls(headerPath, dir, func(line string) (*Syscall, error) {
		matches := armUnistdSycallRegex.FindStringSubmatch(line)
		if len(matches) != 3 || matches[1] == sentinel || omit[matches[1]] {
			return nil, nil
		}

		num, err := strconv.Atoi(matches[2])
		if err != nil {
			return nil, fmt.Errorf("failed to parse syscall number: %v at '%v'", err, line)
		}

		return &Syscall{
			Num:  num,
			Name: matches[1],
		}, nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(syscalls, func(i, j int) bool {
		return syscalls[i].Num < syscalls[j].Num
	})

	return &Arch{
		Name:     "AARCH64",
		Syscalls: syscalls,
	}, nil
}

func build386(dir string) (*Arch, error) {
	const path = "/arch/x86/entry/syscalls/syscall_32.tbl"

	syscalls, err := readSyscalls(path, dir, func(line string) (*Syscall, error) {
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			return nil, nil
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("unexpected line format: %v", line)
		}

		num, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse syscall number: %v at '%v'", err, line)
		}

		return &Syscall{
			Num:  num,
			Name: fields[2],
		}, nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(syscalls, func(i, j int) bool {
		return syscalls[i].Num < syscalls[j].Num
	})

	return &Arch{
		Name:     "386",
		Syscalls: syscalls,
	}, nil
}

func buildX32(dir string) (*Arch, error) {
	const path = "/arch/x86/entry/syscalls/syscall_64.tbl"

	syscalls, err := readSyscalls(path, dir, func(line string) (*Syscall, error) {
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			return nil, nil
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("unexpected line format: %v", line)
		}

		if fields[1] == "x64" {
			return nil, nil
		}

		num, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse syscall number: %v at '%v'", err, line)
		}

		return &Syscall{
			Num:  num,
			Name: fields[2],
		}, nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(syscalls, func(i, j int) bool {
		return syscalls[i].Num < syscalls[j].Num
	})

	return &Arch{
		Name:     "X32",
		Syscalls: syscalls,
	}, nil
}

func buildX86_64(dir string) (*Arch, error) {
	const path = "/arch/x86/entry/syscalls/syscall_64.tbl"

	syscalls, err := readSyscalls(path, dir, func(line string) (*Syscall, error) {
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			return nil, nil
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("unexpected line format: %v", line)
		}

		if fields[1] == "x32" {
			return nil, nil
		}

		num, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse syscall number: %v at '%v'", err, line)
		}

		return &Syscall{
			Num:  num,
			Name: fields[2],
		}, nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(syscalls, func(i, j int) bool {
		return syscalls[i].Num < syscalls[j].Num
	})

	return &Arch{
		Name:     "X86_64",
		Syscalls: syscalls,
	}, nil
}

func readSyscalls(path string, dir string, parse func(line string) (*Syscall, error)) ([]*Syscall, error) {
	url := baseURL + linuxVersion + path

	src, err := downloadFile(url, dir)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var syscalls []*Syscall
	s := bufio.NewScanner(bufio.NewReader(f))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())

		syscall, err := parse(line)
		if err != nil {
			return nil, err
		}
		if syscall != nil {
			syscalls = append(syscalls, syscall)
		}
	}

	return syscalls, s.Err()
}

func downloadFile(url, destinationDir string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("http get failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with http status %v", resp.StatusCode)
	}

	name := filepath.Join(destinationDir, filepath.Base(url))
	f, err := os.Create(name)
	if err != nil {
		return "", fmt.Errorf("failed to create output file: %v", err)
	}

	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to write file to disk: %v", err)
	}

	return name, nil
}

var (
	outputFile   string
	linuxVersion string
)

func init() {
	flag.StringVar(&outputFile, "out", "zsyscalls.go", "output file")
	flag.StringVar(&linuxVersion, "version", "v6.13", "linux version (git tag)")
}

func main() {
	flag.Parse()

	// Make temporary work directory.
	tmp, err := ioutil.TempDir("", "mk_linux_syscalls")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	params := TemplateParams{
		LinuxVersion: linuxVersion,
	}

	// List of all builders.
	builders := []builderFunc{
		buildARM,
		buildAARCH64,
		build386,
		buildX32,
		buildX86_64,
	}

	// Build a syscall table for each architecture.
	for _, b := range builders {
		arch, err := b(tmp)
		if err != nil {
			log.Fatal(err)
		}
		params.Arches = append(params.Arches, *arch)
	}

	// Write the output file based on the template.
	out, err := os.Create(outputFile)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	if err = tmpl.Execute(out, params); err != nil {
		log.Fatal(err)
	}
}
