package storage

import "os"

// fsMkdirAll exists so the rest of the package needs no os import.
func fsMkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }
