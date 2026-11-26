package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// DriveKey derives the key that names a drive in a target: from the server's
// URL, the account on it (empty for a drive shared by several accounts, such as
// a team space) and the drive's id there. The same drive gives the same key on
// every device, and after the server was rebuilt at the same address.
//
// A key must belong to one drive only: two drives sharing a key and a target
// would be offered, and could delete, each other's archived files.
func DriveKey(server, account, drive string) string {
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		// Scheme and host are not case-sensitive; the path is.
		server = strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
	}
	sum := sha256.Sum256([]byte(server + "\x00" + account + "\x00" + drive))
	return hex.EncodeToString(sum[:16])
}
