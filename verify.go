package archive

import (
	"context"
	"errors"
	"fmt"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"github.com/odrazdotxyz/odraz-go-sdk/files"
)

// verify reports whether the object at key holds the content with this SHA-256
// and size, which was also fed to check. false with no error means it does not;
// an error means that could not be found out.
func (a *Archive) verify(ctx context.Context, check files.Check, key, sum string, size int64) (bool, error) {
	if a.opts.Verify == VerifyChecksum {
		if err := check.Verify(ctx, key); err == nil {
			return true, nil
		}
		// Whatever the checksum said (or could not say), the content decides.
	}
	got, n, err := hashFile(ctx, a.dst.FS, key, nil)
	switch {
	case errors.Is(err, odraz.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return got == sum && n == size, nil
}

// mustVerify is verify for a copy that was just made: anything but a yes is
// ErrNotVerified.
func (a *Archive) mustVerify(ctx context.Context, check files.Check, key, sum string, size int64) error {
	ok, err := a.verify(ctx, check, key, sum, size)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrNotVerified, key, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s does not hold the file", ErrNotVerified, key)
	}
	return nil
}
