//go:build ignore

// Run from the integration module: go run ./e2e/testdata/generate.go
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate() (result error) {
	source, err := filepath.Abs("e2e/testdata")
	if err != nil {
		return err
	}
	out, err := os.MkdirTemp("", "l1dtl-fixtures-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(out); result == nil {
			result = err
		}
	}()
	cmd := exec.Command("docker", "run", "--rm",
		"-v", source+":/src:ro", "-v", out+":/out", "--entrypoint", "forge",
		"ghcr.io/foundry-rs/foundry:v1.7.1", "build", "--root", "/src", "--contracts", "/src",
		"--out", "/out", "--cache-path", "/tmp/cache", "--use", "0.8.28", "--evm-version", "cancun", "--optimize", "false", "--no-metadata")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	solidity, err := os.ReadFile(filepath.Join(source, "Fixtures.sol"))
	if err != nil {
		return err
	}
	for _, name := range []string{"AddressManagerFixture", "CTCFixture"} {
		raw, err := os.ReadFile(filepath.Join(out, "Fixtures.sol", name+".json"))
		if err != nil {
			return err
		}
		var artifact struct {
			ABI      json.RawMessage `json:"abi"`
			Bytecode struct {
				Object string `json:"object"`
			} `json:"bytecode"`
		}
		if err := json.Unmarshal(raw, &artifact); err != nil {
			return err
		}
		data, err := json.MarshalIndent(struct {
			Compiler     string          `json:"compiler"`
			SourceSHA256 string          `json:"sourceSHA256"`
			ABI          json.RawMessage `json:"abi"`
			Bytecode     string          `json:"bytecode"`
		}{"solc 0.8.28; evm cancun; optimizer disabled; no metadata", fmt.Sprintf("%x", sha256.Sum256(solidity)), artifact.ABI, artifact.Bytecode.Object}, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(source, name+".json"), append(data, '\n'), 0644); err != nil {
			return err
		}
	}
	return nil
}
