package erofs

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestKernelMount tests that the Linux kernel can mount and read our EROFS images
// This test requires Docker and will be skipped if Docker is not available or
// if the SKIP_KERNEL_TESTS environment variable is set.
func TestKernelMount(t *testing.T) {
	if os.Getenv("SKIP_KERNEL_TESTS") != "" {
		t.Skip("Skipping kernel tests (SKIP_KERNEL_TESTS is set)")
	}

	// Check if docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker not found, skipping kernel tests")
	}

	// Create temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "erofs-kernel-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create input tar with test data
	inputTar := filepath.Join(tmpDir, "input.tar")
	if err := createTestTar(inputTar); err != nil {
		t.Fatalf("Failed to create test tar: %v", err)
	}

	// Convert to EROFS
	erofsImage := filepath.Join(tmpDir, "test.erofs")
	if err := convertTarToErofs(inputTar, erofsImage); err != nil {
		t.Fatalf("Failed to convert tar to EROFS: %v", err)
	}

	// Create output directory
	outputDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatalf("Failed to create output dir: %v", err)
	}

	// Build Docker image
	imageName := "erofs-kernel-test"
	dockerfileDir := "test/kernel"
	if err := buildDockerImage(imageName, dockerfileDir); err != nil {
		t.Fatalf("Failed to build Docker image: %v", err)
	}

	// Run Docker container to mount and extract
	inputDir := filepath.Dir(erofsImage)
	if err := runDockerTest(imageName, inputDir, outputDir); err != nil {
		t.Fatalf("Failed to run Docker test: %v", err)
	}

	// Compare input and output tars
	outputTar := filepath.Join(outputDir, "output.tar")
	if err := compareTars(inputTar, outputTar); err != nil {
		t.Fatalf("Tar comparison failed: %v", err)
	}

	t.Logf("Kernel test passed! Linux kernel successfully mounted and read EROFS image")
}

// createTestTar creates a tar file with various test files
func createTestTar(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	testFiles := []struct {
		name    string
		content string
		mode    int64
	}{
		{"hello.txt", "Hello, World!\n", 0644},
		{"executable.sh", "#!/bin/sh\necho test\n", 0755},
		{"large.txt", strings.Repeat("x", 10000), 0644},
	}

	for _, tf := range testFiles {
		hdr := &tar.Header{
			Name:    tf.name,
			Mode:    tf.mode,
			Size:    int64(len(tf.content)),
			ModTime: time.Unix(1234567890, 0), // Fixed time for reproducibility
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(tf.content)); err != nil {
			return err
		}
	}

	// Add a directory
	hdr := &tar.Header{
		Name:     "subdir/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
		ModTime:  time.Unix(1234567890, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}

	// Add a file in the directory
	hdr = &tar.Header{
		Name:    "subdir/nested.txt",
		Mode:    0644,
		Size:    13,
		ModTime: time.Unix(1234567890, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte("nested file\n\n")); err != nil {
		return err
	}

	return nil
}

// convertTarToErofs converts a tar file to EROFS format
func convertTarToErofs(inputTar, outputErofs string) error {
	tarData, err := os.ReadFile(inputTar)
	if err != nil {
		return fmt.Errorf("reading tar: %w", err)
	}

	erofsData := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarData), erofsData); err != nil {
		return fmt.Errorf("converting to EROFS: %w", err)
	}

	if err := os.WriteFile(outputErofs, erofsData.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing EROFS: %w", err)
	}

	return nil
}

// buildDockerImage builds the test Docker image
func buildDockerImage(imageName, dockerfileDir string) error {
	cmd := exec.Command("docker", "build", "-t", imageName, dockerfileDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runDockerTest runs the Docker container to mount and extract the EROFS image
func runDockerTest(imageName, inputDir, outputDir string) error {
	cmd := exec.Command("docker", "run", "--rm", "--privileged",
		"-v", fmt.Sprintf("%s:/mnt/input", inputDir),
		"-v", fmt.Sprintf("%s:/mnt/output", outputDir),
		imageName,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// compareTars compares two tar files and returns an error if they differ significantly
func compareTars(inputPath, outputPath string) error {
	inputEntries, err := listTarEntries(inputPath)
	if err != nil {
		return fmt.Errorf("listing input tar: %w", err)
	}

	outputEntries, err := listTarEntries(outputPath)
	if err != nil {
		return fmt.Errorf("listing output tar: %w", err)
	}

	// Normalize entry names (remove leading ./)
	normalizeEntries := func(entries []string) []string {
		normalized := make([]string, 0, len(entries))
		for _, e := range entries {
			// Remove leading ./
			e = strings.TrimPrefix(e, "./")
			// Skip "." entry
			if e != "" && e != "." {
				normalized = append(normalized, e)
			}
		}
		return normalized
	}

	inputEntries = normalizeEntries(inputEntries)
	outputEntries = normalizeEntries(outputEntries)

	// Sort for comparison
	sort.Strings(inputEntries)
	sort.Strings(outputEntries)

	if len(inputEntries) != len(outputEntries) {
		return fmt.Errorf("file count mismatch: input=%d, output=%d\nInput: %v\nOutput: %v",
			len(inputEntries), len(outputEntries), inputEntries, outputEntries)
	}

	for i := range inputEntries {
		if inputEntries[i] != outputEntries[i] {
			return fmt.Errorf("file list mismatch at index %d: input=%s, output=%s",
				i, inputEntries[i], outputEntries[i])
		}
	}

	// Compare file contents
	if err := compareContents(inputPath, outputPath); err != nil {
		return fmt.Errorf("content mismatch: %w", err)
	}

	return nil
}

// listTarEntries returns a sorted list of entries in a tar file
func listTarEntries(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tr := tar.NewReader(f)
	var entries []string

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, hdr.Name)
	}

	return entries, nil
}

// compareContents compares the contents of files in two tar archives
func compareContents(inputPath, outputPath string) error {
	inputContents, err := extractTarContents(inputPath)
	if err != nil {
		return fmt.Errorf("extracting input: %w", err)
	}

	outputContents, err := extractTarContents(outputPath)
	if err != nil {
		return fmt.Errorf("extracting output: %w", err)
	}

	// Normalize output keys (remove leading ./)
	normalizedOutput := make(map[string][]byte)
	for name, data := range outputContents {
		normalizedName := strings.TrimPrefix(name, "./")
		normalizedOutput[normalizedName] = data
	}

	for name, inputData := range inputContents {
		outputData, ok := normalizedOutput[name]
		if !ok {
			return fmt.Errorf("file %s not found in output", name)
		}
		if !bytes.Equal(inputData, outputData) {
			return fmt.Errorf("content differs for %s: input=%d bytes, output=%d bytes",
				name, len(inputData), len(outputData))
		}
	}

	return nil
}

// extractTarContents extracts all file contents from a tar archive
func extractTarContents(path string) (map[string][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tr := tar.NewReader(f)
	contents := make(map[string][]byte)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			contents[hdr.Name] = data
		}
	}

	return contents, nil
}
