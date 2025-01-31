// Copyright 2021 The Parca Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package debuginfo

import (
	"debug/elf"
	"debug/gosym"
	"errors"
	"fmt"
	"strings"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/google/pprof/profile"

	"github.com/parca-dev/parca/internal/pprof/binutils"
)

type symbolizer struct {
	logger log.Logger
	bu     *binutils.Binutils
}

func (s *symbolizer) createAddr2Line(m *profile.Mapping, file string) (addr2Line, error) {
	hasDWARF, err := hasDWARF(file)
	if err != nil {
		level.Debug(s.logger).Log(
			"msg", "failed to determine if binary has DWARF info",
			"file", file,
			"err", err,
		)
	}
	if hasDWARF {
		level.Debug(s.logger).Log("msg", "using DWARF to resolve symbols", "file", file)
		return s.compiledBinary(m, file)
	}

	// Go binaries has a special case. They use ".gopclntab" section to symbolize addresses.
	// Keep that section and other identifying sections in the debug information file.
	isGo, err := isGoBinary(file)
	if err != nil {
		level.Debug(s.logger).Log(
			"msg", "failed to determine if binary is a Go binary",
			"file", file,
			"err", err,
		)
	}
	if isGo {
		// Right now, this uses "debug/gosym" package, and it won't work for inlined functions,
		// so this is just a best-effort implementation, in case we don't have DWARF.
		sourceLine, err := s.goBinary(file)
		if err == nil {
			level.Debug(s.logger).Log("msg", "using go addr2Line to resolve symbols", "file", file)
			return sourceLine, nil
		}

		level.Error(s.logger).Log(
			"msg", "failed to create go addr2Line, falling back to binary addr2Line",
			"file", file,
			"err", err,
		)
	}

	// Just in case, underlying binutils can symbolize addresses.
	level.Debug(s.logger).Log("msg", "falling back to binutils addr2Line resolve symbols", "file", file)
	return s.compiledBinary(m, file)
}

func (s *symbolizer) compiledBinary(m *profile.Mapping, file string) (addr2Line, error) {
	objFile, err := s.bu.Open(file, m.Start, m.Limit, m.Offset)
	if err != nil {
		level.Error(s.logger).Log("msg", "failed to open object file",
			"file", file,
			"start", m.Start,
			"limit", m.Limit,
			"offset", m.Offset,
			"err", err,
		)
		return nil, fmt.Errorf("open object file: %w", err)
	}

	return func(addr uint64) ([]profile.Line, error) {
		frames, err := objFile.SourceLine(addr)
		if err != nil {
			level.Debug(s.logger).Log("msg", "failed to open object file",
				"file", file,
				"start", m.Start,
				"limit", m.Limit,
				"offset", m.Offset,
				"address", addr,
				"err", err,
			)
			return nil, err
		}

		if len(frames) == 0 {
			return nil, errors.New("could not find any frames for given address")
		}

		lines := []profile.Line{}
		for _, frame := range frames {
			lines = append(lines, profile.Line{
				Line: int64(frame.Line),
				Function: &profile.Function{
					Name:     frame.Func,
					Filename: frame.File,
				},
			})
		}
		return lines, nil
	}, nil
}

func (s *symbolizer) goBinary(binPath string) (addr2Line, error) {
	level.Debug(s.logger).Log("msg", "symbolizing a Go binary", "file", binPath)
	table, err := gosymtab(binPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create go symbtab: %w", err)
	}

	return func(addr uint64) (lines []profile.Line, err error) {
		defer func() {
			// PCToLine panics with "invalid memory address or nil pointer dereference",
			//	- when it refers to an address that doesn't actually exist.
			if r := recover(); r != nil {
				err = fmt.Errorf("recovering from panic in go binary add2line: %v", r)
			}
		}()

		file, line, fn := table.PCToLine(addr)
		lines = append(lines, profile.Line{
			Line: int64(line),
			Function: &profile.Function{
				Name:     fn.Name,
				Filename: file,
			},
		})
		return lines, nil
	}, nil
}

// Simplified version of rsc.io/goversion/version.
func isGoBinary(path string) (bool, error) {
	exe, err := elf.Open(path)
	if err != nil {
		return false, fmt.Errorf("failed to open elf: %w", err)
	}
	defer exe.Close()

	for _, s := range exe.Sections {
		if s.Name == ".note.go.buildid" {
			return true, nil
		}
	}

	syms, err := exe.Symbols()
	if err != nil {
		return false, fmt.Errorf("failed to read symbols: %w", err)
	}
	for _, sym := range syms {
		name := sym.Name
		if name == "runtime.main" || name == "main.main" {
			return true, nil
		}
		if name == "runtime.buildVersion" {
			return true, nil
		}
	}

	return false, err
}

func hasDWARF(path string) (bool, error) {
	exe, err := elf.Open(path)
	if err != nil {
		return false, fmt.Errorf("failed to open elf: %w", err)
	}
	defer exe.Close()

	data, err := getDWARF(exe)
	if err != nil {
		return false, fmt.Errorf("failed to read DWARF sections: %w", err)
	}

	return len(data) > 0, nil
}

// A simplified and modified version of debug/elf.DWARF().
func getDWARF(f *elf.File) (map[string][]byte, error) {
	dwarfSuffix := func(s *elf.Section) string {
		switch {
		case strings.HasPrefix(s.Name, ".debug_"):
			return s.Name[7:]
		case strings.HasPrefix(s.Name, ".zdebug_"):
			return s.Name[8:]
		case strings.HasPrefix(s.Name, "__debug_"): // macos
			return s.Name[8:]
		default:
			return ""
		}
	}

	// There are many DWARf sections, but these are the ones
	// the debug/dwarf package started with "abbrev", "info", "str", "line", "ranges".
	// Possible canditates for future: "loc", "loclists", "rnglists"
	sections := map[string]*string{"abbrev": nil, "info": nil, "str": nil, "line": nil, "ranges": nil}
	data := map[string][]byte{}
	for _, s := range f.Sections {
		suffix := dwarfSuffix(s)
		if suffix == "" {
			continue
		}
		if _, ok := sections[suffix]; !ok {
			continue
		}
		b, err := s.Data()
		if err != nil {
			return nil, fmt.Errorf("failed to read debug section: %w", err)
		}
		data[suffix] = b
	}

	return data, nil
}

func gosymtab(path string) (*gosym.Table, error) {
	exe, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open elf: %w", err)
	}
	defer exe.Close()

	var pclntab []byte
	if sec := exe.Section(".gopclntab"); sec != nil {
		if sec.Type == elf.SHT_NOBITS {
			return nil, errors.New(".gopclntab section has no bits")
		}

		pclntab, err = sec.Data()
		if err != nil {
			return nil, fmt.Errorf("could not find .gopclntab section: %w", err)
		}
	}

	if len(pclntab) <= 0 {
		return nil, errors.New(".gopclntab section has no bits")
	}

	var symtab []byte
	if sec := exe.Section(".gosymtab"); sec != nil {
		symtab, _ = sec.Data()
	}

	var text uint64 = 0
	if sec := exe.Section(".text"); sec != nil {
		text = sec.Addr
	}

	table, err := gosym.NewTable(symtab, gosym.NewLineTable(pclntab, text))
	if err != nil {
		return nil, fmt.Errorf("failed to build symtab or pclinetab: %w", err)
	}
	return table, nil
}
