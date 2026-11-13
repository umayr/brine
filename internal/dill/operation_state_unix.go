//go:build unix

package dill

import (
	"os"
	"syscall"
)

func operationStateOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
