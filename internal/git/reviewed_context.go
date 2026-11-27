package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func reviewedContext(ctx context.Context, repo string) (context.Context, error) {
	absolute, e := filepath.Abs(repo)
	if e != nil {
		return nil, e
	}
	info, e := os.Lstat(filepath.Join(absolute, ".git"))
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("reviewed repository metadata unavailable")
	}
	return context.WithValue(ctx, reviewedSourceContextKey{}, absolute), nil
}
func (r *Runner) ShortRefReviewed(ctx context.Context, repo string) (string, error) {
	ctx, e := reviewedContext(ctx, repo)
	if e != nil {
		return "", e
	}
	return r.ShortRef(ctx, repo)
}
