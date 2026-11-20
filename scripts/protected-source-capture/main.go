// protected-source-capture is a test-only helper for check-protected-source.sh.
// It assumes a fresh, private fixture directory; it is not a production exporter.
package main

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/umayr/brine/internal/projectsnapshot"
)

func main() {
	if err := capture(); err != nil {
		log.Fatal(err)
	}
}

func capture() error {
	const entrypoint = "compose.pkl"
	want := map[string]string{
		entrypoint:   "184aadbfd344254573ce035b3eaf06f8922b61e3876500a3a4a3e3c86347c490",
		"values.pkl": "6a1c7a252eee149a3d8c3bd5f6025ecb9537121452dae4a63696bdc4c9a3e7dd",
	}
	limits := projectsnapshot.Limits{MaxFiles: 2, MaxEntries: 2, MaxDepth: 1, MaxFileBytes: 4096, MaxTotalBytes: 8192}
	snapshot, err := projectsnapshot.Build("workspace", entrypoint, limits)
	if err != nil {
		return err
	}
	if snapshot.Entrypoint != entrypoint || len(snapshot.Files) != len(want) {
		return fmt.Errorf("unexpected capture entrypoint or file count")
	}
	for _, file := range snapshot.Files {
		digest := sha256.Sum256(file.Content)
		if digest != file.SHA256 || fmt.Sprintf("%x", digest) != want[file.Path] {
			return fmt.Errorf("unexpected capture path or bytes: %q", file.Path)
		}
		delete(want, file.Path)
	}

	// Mutate the live source before publishing, so a late copy of the
	// workspace would disagree with both the captured hash and the receipt.
	if err := os.WriteFile("workspace/values.pkl", []byte("stackName = \"edited-workspace\"\n"), 0644); err != nil {
		return err
	}
	for _, file := range snapshot.Files {
		out, err := os.OpenFile(filepath.Join("export", file.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0444)
		if err != nil {
			return err
		}
		_, writeErr := out.Write(file.Content)
		closeErr := out.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if _, err := fmt.Printf("%x  %s\n", file.SHA256, file.Path); err != nil {
			return err
		}
	}
	return nil
}
