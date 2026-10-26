// Package catalog merges fresh backend inventories and scopes content to registry
// domains and repository paths. A registry domain includes an optional port;
// domain plus repository path forms a repository name, such as
// registry.example:5000/team/app. An image reference adds an optional tag or digest.
// Listings and manifests are scoped to the request authority. Blob lookup uses
// repository membership across all exposed registry domains.
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

// Reference is a parsed image reference. For registry.example:5000/team/app:v1,
// Domain is registry.example:5000 (including the port), Path is team/app, and Tag
// is v1. Digest holds an optional @digest from the reference; it is distinct from
// the backend record's pinned target digest. Tag and Digest may both be present.
type Reference struct {
	Domain, Path string
	Tag          string
	Digest       digest.Digest
}

// ParseReference uses reference.ParseNormalizedNamed for Docker short-name
// expansion, while preserving explicitly written domains. It defaults Tag to
// latest only when neither a tag nor a digest is present.
//
// Examples; <hex> denotes 64 hexadecimal digits:
//
//	alpine                    -> docker.io, library/alpine, latest
//	team/app:v1               -> docker.io, team/app, v1
//	localhost:5000/team/app   -> localhost:5000, team/app, latest
//	quay.io/team/app@sha256:<hex> -> quay.io, team/app, "", sha256:<hex>
func ParseReference(value string) (Reference, error) {
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return Reference{}, err
	}
	// ParseNormalizedNamed expands short names, but also aliases index.docker.io.
	// Explicit domains must retain their exact spelling for exposure matching.
	if first, _, ok := strings.Cut(value, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		parsed, err := reference.Parse(value)
		if err != nil {
			return Reference{}, err
		}
		var ok bool
		named, ok = parsed.(reference.Named)
		if !ok {
			return Reference{}, fmt.Errorf("reference has no repository name")
		}
	}
	parsedRef := Reference{Domain: reference.Domain(named), Path: reference.Path(named)}
	if tagged, ok := named.(reference.Tagged); ok {
		parsedRef.Tag = tagged.Tag()
	} else if _, ok := named.(reference.Digested); !ok {
		parsedRef.Tag = "latest"
	}
	if digested, ok := named.(reference.Digested); ok {
		parsedRef.Digest = digested.Digest()
	}
	return parsedRef, nil
}

// entry pairs a parsed reference with its pinned backend record and tag selection.
type entry struct {
	ref                Reference
	record             image.Record
	backend            image.Backend
	selected, conflict bool
}

// Status reports one backend inventory attempt; Images counts exposed references.
type Status struct {
	Backend, Error string
	Images         int
}

// Row describes an exposed reference in the viewer. StoredDigest identifies the
// backend target before any manifest conversion; Selected refers to tag selection.
type Row struct {
	Domain, Path, Tag, Backend, StoredDigest string
	Selected, Conflict                       bool
}

// View is a snapshot of exposed references and backend status for one request.
type View struct {
	Rows    []Row
	Status  []Status
	Updated time.Time
}

// Catalog retains connections and configuration only. Every operation obtains a
// fresh backend inventory; no image records or content survive the request.
type Catalog struct {
	backends []image.Backend
	domains  map[string]bool
	logger   *slog.Logger
}

func New(backends []image.Backend, domains []string, logger *slog.Logger) *Catalog {
	allowed := make(map[string]bool, len(domains))
	for _, domain := range domains {
		allowed[domain] = true
	}
	return &Catalog{backends: slices.Clone(backends), domains: allowed, logger: logger}
}
func (c *Catalog) AllowsDomain(domain string) bool { return c.domains[domain] }

func (c *Catalog) inventory(ctx context.Context, domain, repositoryPath string) ([]*entry, []Status, error) {
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
				ref, err := ParseReference(record.Reference)
				if err != nil || !c.AllowsDomain(ref.Domain) {
					continue
				}
				status.Images++
				if domain != "" && ref.Domain != domain || repositoryPath != "" && ref.Path != repositoryPath {
					continue
				}
				entries = append(entries, &entry{ref: ref, record: record, backend: backend})
			}
		}
		statuses = append(statuses, status)
	}
	slices.SortFunc(entries, compareEntries)
	// tagKey identifies a mutable tag within one registry repository, for example
	// registry.example:5000/team/app:v1. Digest is excluded so records claiming the
	// same tag compete for selection even when their content differs.
	type tagKey struct {
		domain, repositoryPath, tag string
	}
	winners := make(map[tagKey]*entry)
	for _, e := range entries {
		if e.ref.Tag == "" {
			continue
		}
		key := tagKey{e.ref.Domain, e.ref.Path, e.ref.Tag}
		winner := winners[key]
		if winner == nil {
			e.selected = true
			winners[key] = e
			continue
		}
		if winner.record.Target.Digest != e.record.Target.Digest {
			winner.conflict, e.conflict = true, true
			c.logger.WarnContext(ctx, "conflicting image tag; serving Docker image", "image", tagReference(e.ref), "selected_backend", winner.backend.Name(), "selected_digest", winner.record.Target.Digest, "other_backend", e.backend.Name(), "other_digest", e.record.Target.Digest)
		}
	}
	return entries, statuses, errors.Join(failures...)
}

func compareEntries(a, b *entry) int {
	if n := strings.Compare(tagReference(a.ref), tagReference(b.ref)); n != 0 {
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
func tagReference(ref Reference) string { return ref.Domain + "/" + ref.Path + ":" + ref.Tag }

// uniqueRecords collapses tag aliases for content lookup, preserving backend
// priority. Identity includes the pinned target, so retagged records stay distinct.
func uniqueRecords(entries []*entry) []*entry {
	// identity collapses aliases only when backend, immutable ID, and pinned
	// target digest all match. A reference name alone cannot identify content.
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

// Repositories lists repository paths within an exposed registry domain.
func (c *Catalog) Repositories(ctx context.Context, domain string) ([]string, error) {
	if !c.AllowsDomain(domain) {
		return nil, image.ErrNotFound
	}
	entries, _, err := c.inventory(ctx, domain, "")
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.ref.Path] = true
	}
	return sortedKeys(names), nil
}

func (c *Catalog) Tags(ctx context.Context, domain, repositoryPath string) ([]string, error) {
	if !c.AllowsDomain(domain) {
		return nil, image.ErrNotFound
	}
	entries, _, err := c.inventory(ctx, domain, repositoryPath)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, image.ErrNotFound
	}
	tags := make(map[string]bool)
	for _, e := range entries {
		if e.selected {
			tags[e.ref.Tag] = true
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

// Manifest resolves a tag or digest within one registry domain and repository path.
func (c *Catalog) Manifest(ctx context.Context, domain, repositoryPath, tagOrDigest string) (image.Manifest, error) {
	if !c.AllowsDomain(domain) {
		return image.Manifest{}, image.ErrNotFound
	}
	entries, _, err := c.inventory(ctx, domain, repositoryPath)
	if err != nil {
		return image.Manifest{}, err
	}
	if !strings.Contains(tagOrDigest, ":") {
		for _, e := range entries {
			if !e.selected || e.ref.Tag != tagOrDigest {
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
	d, err := digest.Parse(tagOrDigest)
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

// Blob resolves content through an exposed image with the given repository path,
// regardless of the request authority. The same record must satisfy exposure,
// repository path, and digest membership; cached content alone never establishes
// membership.
func (c *Catalog) Blob(ctx context.Context, repositoryPath string, d digest.Digest) (*image.BlobRef, error) {
	if repositoryPath == "" {
		return nil, image.ErrNotFound
	}
	if err := d.Validate(); err != nil {
		return nil, image.ErrNotFound
	}
	entries, _, err := c.inventory(ctx, "", repositoryPath)
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
		view.Rows = append(view.Rows, Row{Domain: e.ref.Domain, Path: e.ref.Path, Tag: e.ref.Tag, Backend: e.backend.Name(), StoredDigest: e.record.Target.Digest.String(), Selected: e.selected, Conflict: e.conflict})
	}
	return view
}
