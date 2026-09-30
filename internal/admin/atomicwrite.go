package admin

import (
	"os"

	"github.com/egerkuzma/kuztds/internal/atomicfile"
)

// writeFileAtomic is atomicfile.Write; the reasoning lives there.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}
