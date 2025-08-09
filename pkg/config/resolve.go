package config

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/inercia/MCPShell/pkg/common"
)

// ResolveConfigPath tries to resolve the configuration file path.
// If the path is a URL, it downloads the file to a temporary location.
// If the path is a directory, it returns all YAML files in that directory.
// The function returns the local path(s) to the configuration file(s) and a cleanup function
// that should be deferred to remove any temporary files.
func ResolveConfigPath(configPath string, logger *common.Logger) (string, func(), error) {
	// Default no-op cleanup function
	noopCleanup := func() {}

	// Return early if config path is empty
	if configPath == "" {
		return "", noopCleanup, fmt.Errorf("configuration file path is empty")
	}

	// Check if the configPath is a URL
	parsedURL, err := url.Parse(configPath)
	if err != nil {
		return "", noopCleanup, fmt.Errorf("invalid configuration path: %w", err)
	}

	// If it's not a URL, check if it's a local file or directory
	if parsedURL.Scheme == "" || parsedURL.Scheme == "file" {
		localPath := configPath
		if parsedURL.Scheme == "file" {
			localPath = parsedURL.Path
		}

		// Check if the path exists
		fileInfo, err := os.Stat(localPath)
		if os.IsNotExist(err) {
			return "", noopCleanup, fmt.Errorf("configuration path does not exist: %s", localPath)
		}

		// If it's a directory, resolve all YAML files in it
		if fileInfo.IsDir() {
			return resolveConfigDirectory(localPath, logger)
		}

		// If it's a file, verify it's a YAML file
		if !strings.HasSuffix(strings.ToLower(localPath), ".yaml") && !strings.HasSuffix(strings.ToLower(localPath), ".yml") {
			return "", noopCleanup, fmt.Errorf("configuration file must have .yaml or .yml extension: %s", localPath)
		}

		logger.Info("Using local configuration file: %s", localPath)
		return localPath, noopCleanup, nil
	}

	// If it's a remote URL, download it
	if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		logger.Info("Downloading configuration from URL: %s", configPath)

		// Create a temporary file
		tmpDir := os.TempDir()
		tmpFile, err := os.CreateTemp(tmpDir, "mcp-config-*.yaml")
		if err != nil {
			return "", noopCleanup, fmt.Errorf("failed to create temporary file: %w", err)
		}
		tmpFilePath := tmpFile.Name()

		// Create cleanup function for the temporary file
		cleanup := func() {
			if err := tmpFile.Close(); err != nil {
				logger.Error("Failed to close temporary file: %v", err)
			}
			if err := os.Remove(tmpFilePath); err != nil {
				logger.Error("Failed to remove temporary file: %v", err)
			}
			logger.Debug("Cleaned up temporary configuration file: %s", tmpFilePath)
		}

		// Download the file
		resp, err := http.Get(configPath)
		if err != nil {
			cleanup()
			return "", noopCleanup, fmt.Errorf("failed to download configuration: %w", err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				logger.Error("Failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			cleanup()
			return "", noopCleanup, fmt.Errorf("failed to download configuration, status code: %d", resp.StatusCode)
		}

		// Copy the response body to the temporary file
		_, err = io.Copy(tmpFile, resp.Body)
		if err != nil {
			cleanup()
			return "", noopCleanup, fmt.Errorf("failed to write configuration to temporary file: %w", err)
		}

		// Close the file after writing
		if err = tmpFile.Close(); err != nil {
			cleanup()
			return "", noopCleanup, fmt.Errorf("failed to close temporary file: %w", err)
		}

		logger.Info("Downloaded configuration to temporary file: %s", tmpFilePath)
		return tmpFilePath, cleanup, nil
	}

	return "", noopCleanup, fmt.Errorf("unsupported URL scheme: %s", parsedURL.Scheme)
}

// resolveConfigDirectory finds all YAML files in a directory and creates a merged configuration file.
// Returns the path to the merged configuration file and a cleanup function.
func resolveConfigDirectory(dirPath string, logger *common.Logger) (string, func(), error) {
	logger.Info("Scanning directory for YAML configuration files: %s", dirPath)

	// Find all YAML files in the directory
	var yamlFiles []string
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Check if it's a YAML file
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yaml" || ext == ".yml" {
			yamlFiles = append(yamlFiles, path)
			logger.Debug("Found YAML file: %s", path)
		}

		return nil
	})

	if err != nil {
		return "", func() {}, fmt.Errorf("failed to scan directory: %w", err)
	}

	if len(yamlFiles) == 0 {
		return "", func() {}, fmt.Errorf("no YAML files found in directory: %s", dirPath)
	}

	logger.Info("Found %d YAML files in directory", len(yamlFiles))

	// If there's only one file, return it directly
	if len(yamlFiles) == 1 {
		logger.Info("Using single configuration file: %s", yamlFiles[0])
		return yamlFiles[0], func() {}, nil
	}

	// Create a merged configuration file
	return createMergedConfigFile(yamlFiles, logger)
}

// createMergedConfigFile creates a temporary file containing the merged configuration
// from multiple YAML files. Returns the path to the merged file and a cleanup function.
func createMergedConfigFile(yamlFiles []string, logger *common.Logger) (string, func(), error) {
	// Create a temporary file for the merged configuration
	tmpDir := os.TempDir()
	tmpFile, err := os.CreateTemp(tmpDir, "mcp-config-merged-*.yaml")
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create temporary merged config file: %w", err)
	}
	tmpFilePath := tmpFile.Name()

	// Create cleanup function
	cleanup := func() {
		if tmpFile != nil {
			if err := tmpFile.Close(); err != nil {
				logger.Error("Failed to close temporary merged config file: %v", err)
			}
		}
		if err := os.Remove(tmpFilePath); err != nil {
			logger.Error("Failed to remove temporary merged config file: %v", err)
		}
		logger.Debug("Cleaned up temporary merged configuration file: %s", tmpFilePath)
	}

	// Load and merge all configurations
	mergedConfig, err := LoadAndMergeConfigs(yamlFiles)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to merge configuration files: %w", err)
	}

	// Write the merged configuration to the temporary file
	data, err := mergedConfig.ToYAML()
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to serialize merged configuration: %w", err)
	}

	_, err = tmpFile.Write(data)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to write merged configuration to temporary file: %w", err)
	}

	if err = tmpFile.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to close temporary merged config file: %w", err)
	}

	logger.Info("Created merged configuration file: %s (from %d source files)", tmpFilePath, len(yamlFiles))
	return tmpFilePath, cleanup, nil
}
