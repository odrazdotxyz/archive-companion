package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"github.com/odrazdotxyz/odraz-go-sdk/files"
)

// The drive's manifest says, among other things, which target its archive is in,
// so another device knows what to ask the user to connect. Its rules are those
// of the gateway's docs/manifest.md: everything this code does not know is kept,
// and a manifest it cannot read is never overwritten.
const (
	manifestPath    = reserved + "/manifest.json"
	manifestVersion = 1
	sectionName     = "archive"
)

// ErrManifest: the drive's manifest is broken or newer than this code.
var ErrManifest = errors.New("archive: the drive's manifest cannot be read or is newer than this version")

type object = map[string]json.RawMessage

// check refuses a descriptor that could carry a credential.
func (d TargetDescriptor) check() error {
	// User info, a query or a fragment. The endpoint is not repeated here.
	if strings.ContainsAny(d.Endpoint, "@?#") {
		return fmt.Errorf("archive: the endpoint must not contain a user name, key, query or fragment: %w", odraz.ErrInvalidPath)
	}
	return nil
}

// readManifest returns the manifest's top level and its sections; both are empty
// when there is no manifest.
func readManifest(ctx context.Context, fs files.FS) (top, sections object, err error) {
	top, sections = object{}, object{}
	st, err := fs.Stat(ctx, manifestPath)
	if errors.Is(err, odraz.ErrNotFound) {
		return top, sections, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if st.IsDir || st.Size > maxRecord {
		return nil, nil, ErrManifest
	}
	rc, err := fs.Read(ctx, manifestPath, 0, -1)
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxRecord+1))
	if err != nil {
		return nil, nil, err
	}
	var version int
	if len(data) > maxRecord || json.Unmarshal(data, &top) != nil || json.Unmarshal(top["version"], &version) != nil || version != manifestVersion {
		return nil, nil, ErrManifest
	}
	if raw, ok := top["sections"]; ok {
		if json.Unmarshal(raw, &sections) != nil {
			return nil, nil, ErrManifest
		}
	}
	if sections == nil {
		sections = object{}
	}
	return top, sections, nil
}

// writeManifest stores the manifest, or removes it when nothing is left in it.
func writeManifest(ctx context.Context, fs files.FS, top, sections object) error {
	delete(top, "sections")
	delete(top, "version")
	if len(top) == 0 && len(sections) == 0 {
		if err := fs.Delete(ctx, manifestPath, false); err != nil && !errors.Is(err, odraz.ErrNotFound) {
			return err
		}
		err := fs.Delete(ctx, reserved, false)
		if errors.Is(err, odraz.ErrNotFound) || errors.Is(err, odraz.ErrNotEmpty) {
			return nil
		}
		return err
	}
	raw, err := json.Marshal(sections)
	if err != nil {
		return err
	}
	top["version"], top["sections"] = json.RawMessage("1"), raw
	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxRecord {
		return ErrManifest
	}
	if err := fs.Mkdir(ctx, reserved); err != nil {
		return err
	}
	return fs.Write(ctx, manifestPath, bytes.NewReader(data), files.WriteOptions{Size: int64(len(data)), Overwrite: true})
}

// ReadTarget returns the archive target the drive's manifest names, if any.
func ReadTarget(ctx context.Context, fs files.FS) (TargetDescriptor, bool, error) {
	var d TargetDescriptor
	_, sections, err := readManifest(ctx, fs)
	if err != nil {
		return d, false, err
	}
	raw, ok := sections[sectionName]
	if !ok {
		return d, false, nil
	}
	if json.Unmarshal(raw, &d) != nil || d.check() != nil {
		return TargetDescriptor{}, false, ErrManifest
	}
	return d, true, nil
}

// SetTarget records the drive's archive target in its manifest.
func SetTarget(ctx context.Context, fs files.FS, d TargetDescriptor) error {
	if err := d.check(); err != nil {
		return err
	}
	top, sections, err := readManifest(ctx, fs)
	if err != nil {
		return err
	}
	section := object{}
	if raw, ok := sections[sectionName]; ok && (json.Unmarshal(raw, &section) != nil || section == nil) {
		return ErrManifest
	}
	for k, v := range map[string]string{"endpoint": d.Endpoint, "bucket": d.Bucket, "prefix": d.Prefix} {
		section[k], _ = json.Marshal(v)
	}
	if sections[sectionName], err = json.Marshal(section); err != nil {
		return err
	}
	return writeManifest(ctx, fs, top, sections)
}

// RemoveTarget takes the archive target out of the drive's manifest.
func RemoveTarget(ctx context.Context, fs files.FS) error {
	top, sections, err := readManifest(ctx, fs)
	if err != nil {
		return err
	}
	delete(sections, sectionName)
	return writeManifest(ctx, fs, top, sections)
}
