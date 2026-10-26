// Package catalog merges backend inventories and scopes content to registry repositories.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
	"regview/internal/image"
)

type Name struct{ Host, Repository, Tag string }

// ParseName uses standard Docker short-name expansion, while preserving explicit authorities.
func ParseName(value string) (Name, error) {
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return Name{}, err
	}
	// ParseNormalizedNamed expands short names, but also aliases index.docker.io.
	// Explicit authorities must retain their exact spelling for Host matching.
	if first, _, ok := strings.Cut(value, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		parsed, err := reference.Parse(value)
		if err != nil {
			return Name{}, err
		}
		var ok bool
		named, ok = parsed.(reference.Named)
		if !ok {
			return Name{}, fmt.Errorf("reference has no repository name")
		}
	}
	name := Name{Host: reference.Domain(named), Repository: reference.Path(named)}
	if tagged, ok := named.(reference.Tagged); ok {
		name.Tag = tagged.Tag()
	} else if _, ok := named.(reference.Digested); !ok {
		name.Tag = "latest"
	}
	return name, nil
}

type entry struct {
	name               Name
	record             image.Record
	backend            image.Backend
	selected, conflict bool
}

type Status struct {
	Backend, Error string
	Images         int
}
type Row struct {
	Host, Repository, Tag, Backend, OriginalDigest string
	Selected, Conflict                             bool
}
type View struct {
	Rows    []Row
	Status  []Status
	Updated time.Time
}

// Catalog retains connections and configuration only. Every operation obtains a
// fresh backend inventory; no image records or content survive the request.
type Catalog struct {
	backends []image.Backend
	hosts    map[string]bool
	logger   *slog.Logger
}

func New(backends []image.Backend, hosts []string, logger *slog.Logger) *Catalog {
	allowed := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		allowed[host] = true
	}
	return &Catalog{backends: slices.Clone(backends), hosts: allowed, logger: logger}
}
func (c *Catalog) Allows(host string) bool { return c.hosts[host] }

func (c *Catalog) inventory(ctx context.Context, host, repo string) ([]*entry, []Status, error) {
	var entries []*entry
	var statuses []Status
	var failures []error
	for _, backend := range c.backends {
		if err := ctx.Err(); err != nil {
			return nil, statuses, err
		}
		records, err := backend.List(ctx)
		status := Status{Backend: backend.Name()}
		if err != nil {
			status.Error = err.Error()
			failures = append(failures, fmt.Errorf("%s: %w", backend.Name(), err))
		} else {
			for _, record := range records {
				name, err := ParseName(record.Reference)
				if err != nil || !c.Allows(name.Host) {
					continue
				}
				status.Images++
				if host != "" && name.Host != host || repo != "" && name.Repository != repo {
					continue
				}
				entries = append(entries, &entry{name: name, record: record, backend: backend})
			}
		}
		statuses = append(statuses, status)
	}
	slices.SortFunc(entries, compareEntries)
	winners := make(map[Name]*entry)
	for _, e := range entries {
		if e.name.Tag == "" {
			continue
		}
		winner := winners[e.name]
		if winner == nil {
			e.selected = true
			winners[e.name] = e
			continue
		}
		if winner.record.Target.Digest != e.record.Target.Digest {
			winner.conflict, e.conflict = true, true
			c.logger.WarnContext(ctx, "conflicting image tag; serving Docker image", "image", tagKey(e.name), "selected_backend", winner.backend.Name(), "selected_digest", winner.record.Target.Digest, "other_backend", e.backend.Name(), "other_digest", e.record.Target.Digest)
		}
	}
	return entries, statuses, errors.Join(failures...)
}

func compareEntries(a, b *entry) int {
	if n := strings.Compare(tagKey(a.name), tagKey(b.name)); n != 0 {
		return n
	}
	if a.backend.Name() != b.backend.Name() {
		if a.backend.Name() == "docker" {
			return -1
		}
		if b.backend.Name() == "docker" {
			return 1
		}
		return strings.Compare(a.backend.Name(), b.backend.Name())
	}
	return strings.Compare(a.record.ID, b.record.ID)
}
func tagKey(n Name) string { return n.Host + "/" + n.Repository + ":" + n.Tag }

// uniqueRecords collapses tag aliases for content lookup, preserving backend
// priority. Identity includes the pinned target, so retagged records stay distinct.
func uniqueRecords(entries []*entry) []*entry {
	type identity struct {
		backend, id string
		digest      digest.Digest
	}
	seen := make(map[identity]bool, len(entries))
	var records []*entry
	for _, e := range entries {
		key := identity{e.backend.Name(), e.record.ID, e.record.Target.Digest}
		if !seen[key] {
			seen[key] = true
			records = append(records, e)
		}
	}
	return records
}

func (c *Catalog) Repositories(ctx context.Context, host string) ([]string, error) {
	if !c.Allows(host) {
		return nil, image.ErrNotFound
	}
	entries, _, err := c.inventory(ctx, host, "")
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.name.Repository] = true
	}
	return sortedKeys(names), nil
}

func (c *Catalog) Tags(ctx context.Context, host, repo string) ([]string, error) {
	if !c.Allows(host) {
		return nil, image.ErrNotFound
	}
	entries, _, err := c.inventory(ctx, host, repo)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, image.ErrNotFound
	}
	tags := make(map[string]bool)
	for _, e := range entries {
		if e.selected {
			tags[e.name.Tag] = true
		}
	}
	return sortedKeys(tags), nil
}
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (c *Catalog) Manifest(ctx context.Context, host, repo, ref string) (image.Manifest, error) {
	if !c.Allows(host) {
		return image.Manifest{}, image.ErrNotFound
	}
	entries, _, err := c.inventory(ctx, host, repo)
	if err != nil {
		return image.Manifest{}, err
	}
	if !strings.Contains(ref, ":") {
		for _, e := range entries {
			if !e.selected || e.name.Tag != ref {
				continue
			}
			a, err := e.backend.Prepare(ctx, e.record)
			if err != nil {
				return image.Manifest{}, err
			}
			manifest := a.Manifests[a.Root.Digest]
			if err := a.Close(); err != nil {
				return image.Manifest{}, err
			}
			return manifest, nil
		}
		return image.Manifest{}, image.ErrNotFound
	}
	d, err := digest.Parse(ref)
	if err != nil {
		return image.Manifest{}, image.ErrNotFound
	}
	var failures []error
	for _, e := range uniqueRecords(entries) {
		a, err := e.backend.Prepare(ctx, e.record)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		manifest, ok := a.Manifests[d]
		closeErr := a.Close()
		if closeErr != nil {
			return image.Manifest{}, closeErr
		}
		if ok {
			return manifest, nil
		}
	}
	if len(failures) > 0 {
		return image.Manifest{}, errors.Join(failures...)
	}
	return image.Manifest{}, image.ErrNotFound
}

// Blob resolves content through an exposed image in repo, regardless of the
// request authority. The same record must satisfy exposure, repository, and
// digest membership; cached content alone never establishes membership.
func (c *Catalog) Blob(ctx context.Context, repo string, d digest.Digest) (*image.BlobRef, error) {
	if repo == "" {
		return nil, image.ErrNotFound
	}
	if err := d.Validate(); err != nil {
		return nil, image.ErrNotFound
	}
	entries, _, err := c.inventory(ctx, "", repo)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, e := range uniqueRecords(entries) {
		resolved, err := e.backend.ResolveBlob(ctx, e.record, d)
		if errors.Is(err, image.ErrNotFound) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		return resolved, nil
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	return nil, image.ErrNotFound
}

func (c *Catalog) View(ctx context.Context) View {
	entries, statuses, _ := c.inventory(ctx, "", "")
	view := View{Status: statuses, Updated: time.Now()}
	for _, e := range entries {
		view.Rows = append(view.Rows, Row{Host: e.name.Host, Repository: e.name.Repository, Tag: e.name.Tag, Backend: e.backend.Name(), OriginalDigest: e.record.Target.Digest.String(), Selected: e.selected, Conflict: e.conflict})
	}
	return view
}
