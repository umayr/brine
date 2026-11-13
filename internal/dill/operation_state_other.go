//go:build !unix

package dill

import "os"

func operationStateOwned(os.FileInfo) bool { return false }
