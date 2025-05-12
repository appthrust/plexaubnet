/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package testutils provides testing utilities for Aquanaut controllers
package testutils

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// getK8sVersion returns the Kubernetes version to use
// If environment variable ENVTEST_K8S_VERSION is set, it uses that value,
// otherwise returns the compatible default version 1.32.0
func getK8sVersion() string {
	version := os.Getenv("ENVTEST_K8S_VERSION")
	if version == "" {
		// Default is 1.32.0, compatible with k8s.io/api v0.32.x
		return "1.32.0"
	}
	// In Makefile, it's typically set in the format "1.32",
	// so convert to major.minor.0 format
	if !strings.Contains(version, ".") {
		return version + ".0"
	}
	parts := strings.Split(version, ".")
	if len(parts) == 2 {
		return version + ".0"
	}
	return version
}

// BinaryAssetsDir is the directory where envtest binaries are stored
var BinaryAssetsDir = filepath.Join(os.TempDir(), ".cache", "kubebuilder-envtest")

func init() {
	// Skip if KUBEBUILDER_ASSETS is already set
	if os.Getenv("KUBEBUILDER_ASSETS") != "" {
		fmt.Println("Using existing KUBEBUILDER_ASSETS:", os.Getenv("KUBEBUILDER_ASSETS"))
		return
	}

	fmt.Println("Setting up envtest assets in:", BinaryAssetsDir)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(BinaryAssetsDir, 0o755); err != nil {
		panic(fmt.Errorf("failed to create envtest directory: %w", err))
	}

	// Check if etcd binary exists
	etcdPath := filepath.Join(BinaryAssetsDir, "etcd")
	if runtime.GOOS == "windows" {
		etcdPath = etcdPath + ".exe"
	}

	// If binaries already exist, just set the environment variable
	if _, err := os.Stat(etcdPath); err == nil {
		err := os.Setenv("KUBEBUILDER_ASSETS", BinaryAssetsDir)
		if err != nil {
			panic(fmt.Errorf("failed to set KUBEBUILDER_ASSETS: %w", err))
		}
		fmt.Println("KUBEBUILDER_ASSETS set to:", BinaryAssetsDir)
		return
	}

	// Download binaries using setup-envtest
	k8sVersion := getK8sVersion()
	fmt.Printf("Downloading K8s %s binaries using setup-envtest...\n", k8sVersion)
	cmd := exec.Command("go", "run", "sigs.k8s.io/controller-runtime/tools/setup-envtest@latest",
		"use", k8sVersion, "-p", "path", "--bin-dir", BinaryAssetsDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// If the first attempt fails, try with installed versions
		fmt.Printf("First attempt failed, trying with installed versions...\n")

		// Reset stdout/stderr
		stdout.Reset()
		stderr.Reset()

		fallbackCmd := exec.Command("go", "run", "sigs.k8s.io/controller-runtime/tools/setup-envtest@latest",
			"use", "-i", "--use-env", "-p", "path", "--bin-dir", BinaryAssetsDir)

		fallbackCmd.Stdout = &stdout
		fallbackCmd.Stderr = &stderr

		if fallbackErr := fallbackCmd.Run(); fallbackErr != nil {
			panic(fmt.Errorf("failed to download envtest binaries: %w\nStdout: %s\nStderr: %s",
				err, stdout.String(), stderr.String()))
		}

		// Get asset path from fallback result
		assetsPath := strings.TrimSpace(stdout.String())
		validateAndSetEnvPath(assetsPath)
	} else {
		// If executed successfully, get asset path from standard output
		assetsPath := strings.TrimSpace(stdout.String())
		validateAndSetEnvPath(assetsPath)
	}
}

// validateAndSetEnvPath validates that the path output by setup-envtest is valid
// and sets it to the KUBEBUILDER_ASSETS environment variable
func validateAndSetEnvPath(path string) {
	if path == "" {
		panic(fmt.Errorf("empty path received from setup-envtest"))
	}

	// Check if etcd binary exists in the path
	etcdPath := filepath.Join(path, "etcd")
	if runtime.GOOS == "windows" {
		etcdPath = etcdPath + ".exe"
	}

	if _, err := os.Stat(etcdPath); err != nil {
		panic(fmt.Errorf("etcd binary not found at expected path %s: %w", etcdPath, err))
	}

	fmt.Println("Successfully downloaded envtest binaries to:", path)
	err := os.Setenv("KUBEBUILDER_ASSETS", path)
	if err != nil {
		panic(fmt.Errorf("failed to set KUBEBUILDER_ASSETS: %w", err))
	}
	fmt.Println("KUBEBUILDER_ASSETS set to:", path)
}
