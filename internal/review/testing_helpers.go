// SPDX-License-Identifier: AGPL-3.0-or-later

package review

// SetUserCacheDirForTest replaces the userCacheDir function for the duration
// of a test. The returned function restores the original; pass it to
// t.Cleanup. This is exported so external test packages (e.g. server_test)
// can redirect the cache without touching $HOME or $XDG_CACHE_HOME.
//
// This function is intentionally not guarded by a build tag: keeping it in a
// regular file avoids the complexity of build-tag coordination across packages
// while the name makes its test-only purpose clear.
func SetUserCacheDirForTest(fn func() (string, error)) (restore func()) {
	original := userCacheDir
	userCacheDir = fn
	return func() { userCacheDir = original }
}
