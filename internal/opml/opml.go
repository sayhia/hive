// Package opml handles OPML import/export — the portable subscription-list
// format every reader supports, so users can migrate in from (and out to)
// Reeder, Feedly, etc.
//
// Ported from papr's src-tauri/src/opml.rs. Where the Rust version leans on the
// `opml` crate's strict parser, this package uses encoding/xml directly:
// encoding/xml decodes well-formed XML entities in attribute values for us (so
// `&amp;` → `&`, `&#39;` → `'`), matching what the `opml` crate exposes after
// parsing.
package opml

import (
	"encoding/xml"
	"sort"
	"strings"

	"hive/internal/apperr"
	"hive/internal/db"
)

// Entry is a subscription parsed out of an OPML file. A nil Folder means the
// feed sits at the top level (mirrors Rust's ImportedFeed with folder: None).
type Entry struct {
	Title  string
	XMLURL string
	Folder *string
}

// outline mirrors a single <outline> element on the parse side. encoding/xml
// decodes XML entity references in these attribute values automatically, so
// `text`/`title`/`xmlUrl` arrive already unescaped (the same shape the `opml`
// crate hands back). An absent attribute decodes to the zero value (""); since
// outlineLabel treats a blank (whitespace-only or empty) label as absent, a
// missing `title` and title="" collapse to the same result.
type outline struct {
	Text     string    `xml:"text,attr"`
	Title    string    `xml:"title,attr"`
	XMLURL   string    `xml:"xmlUrl,attr"`
	Outlines []outline `xml:"outline"`
}

type opmlDoc struct {
	XMLName xml.Name `xml:"opml"`
	Body    struct {
		Outlines []outline `xml:"outline"`
	} `xml:"body"`
}

// Parse parses an OPML document into a flat list of feeds with their folder
// names.
//
// The document is run through tidy first: real-world OPML exports (Readwise
// Reader and many others) routinely emit bare `&` characters inside feed URLs
// (`?type=etoc&feed=rss`) and titles (`Cell Death & Disease`) rather than the
// well-formed `&amp;`. A strict XML parser rejects the *entire* document on the
// first such `&`, so without this step a single stray ampersand anywhere
// silently fails the whole import.
func Parse(content string) ([]Entry, error) {
	var doc opmlDoc
	if err := xml.Unmarshal([]byte(tidy(content)), &doc); err != nil {
		return nil, apperr.Wrap("opml", err)
	}
	// encoding/xml is lenient: it will happily ignore arbitrary leading text and
	// return a zero document rather than erroring. The Rust `opml` crate rejects
	// such input. Require a recognizable <opml> root so malformed input fails as
	// it does in papr.
	if doc.XMLName.Local != "opml" {
		return nil, apperr.Code("opml")
	}

	// Real-world OPML exports routinely repeat the same feed URL across folders
	// (NetNewsWire dumps its "Today" group, "All Articles", and "Unread" views
	// from the same source). Deduplicate by xmlUrl and keep the first occurrence
	// (preserves the user's original folder).
	seen := make(map[string]struct{})
	var feeds []Entry
	var collectInto []Entry
	for i := range doc.Body.Outlines {
		collect(&doc.Body.Outlines[i], nil, &collectInto)
	}
	for _, feed := range collectInto {
		if _, ok := seen[feed.XMLURL]; !ok {
			seen[feed.XMLURL] = struct{}{}
			feeds = append(feeds, feed)
		}
	}
	return feeds, nil
}

// tidy escapes bare `&` — those not already opening a valid XML entity — into
// `&amp;`, so an otherwise non-well-formed real-world OPML file parses instead
// of being rejected wholesale. A `&` that already begins a valid entity is left
// as-is, so a correctly-escaped file round-trips unchanged (no `&amp;` →
// `&amp;amp;`).
func tidy(content string) string {
	var out strings.Builder
	out.Grow(len(content) + 32)
	for i := 0; i < len(content); i++ {
		c := content[i]
		if c == '&' && !startsValidEntity(content[i+1:]) {
			out.WriteString("&amp;")
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}

// startsValidEntity reports whether rest (the text immediately following a `&`)
// begins a valid XML entity reference — a named entity
// (`amp;`/`lt;`/`gt;`/`quot;`/`apos;`) or a numeric one (`#123;` / `#x1F;`).
// Used by tidy to leave already-escaped `&` alone.
func startsValidEntity(rest string) bool {
	for _, name := range []string{"amp;", "lt;", "gt;", "quot;", "apos;"} {
		if strings.HasPrefix(rest, name) {
			return true
		}
	}
	after, ok := strings.CutPrefix(rest, "#")
	if !ok {
		return false
	}
	body := after
	hex := false
	if b, ok := stripPrefixAny(after, "xX"); ok {
		body, hex = b, true
	}
	semi := strings.IndexByte(body, ';')
	if semi < 0 {
		return false
	}
	if semi == 0 {
		return false
	}
	for _, c := range body[:semi] {
		if hex {
			if !isASCIIHexDigit(c) {
				return false
			}
		} else {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func stripPrefixAny(s, cutset string) (string, bool) {
	if s == "" {
		return s, false
	}
	if strings.IndexByte(cutset, s[0]) >= 0 {
		return s[1:], true
	}
	return s, false
}

func isASCIIHexDigit(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// outlineLabel returns the human-facing label of an outline: its `text`
// attribute, falling back to the `title` attribute. Many OPML exporters label
// folder/feed outlines with only `title` (which the spec permits), leaving
// `text` empty.
//
// A *whitespace-only* attribute counts as absent, not present: a folder outline
// labelled `text=" "` would otherwise pass its blank name down to
// db.CreateFolder (via import → folderIDByName), which trims it to "" — and
// db.CreateFolder rejects an empty name, so the whole transactional import would
// abort on one stray outline. Treating a blank label as absent here keeps such a
// feed importing as ungrouped (and a blank-labelled feed outline falling back to
// its URL for a title) instead.
//
// Returns the label and whether one was present (ok=false ≈ Rust's None).
func outlineLabel(o *outline) (string, bool) {
	var label string
	if strings.TrimSpace(o.Text) != "" {
		label = o.Text
	} else {
		label = o.Title
	}
	if strings.TrimSpace(label) == "" {
		return "", false
	}
	return label, true
}

func collect(o *outline, folder *string, out *[]Entry) {
	if o.XMLURL != "" {
		title := o.XMLURL
		if label, ok := outlineLabel(o); ok {
			title = label
		}
		*out = append(*out, Entry{
			XMLURL: o.XMLURL,
			Title:  title,
			Folder: clonePtr(folder),
		})
	}
	// A childless-of-feeds outline acts as a folder for its descendants. Use the
	// same `text`-then-`title` label resolution feeds get — otherwise a folder
	// outline labelled only with `title` would import its feeds into a folder
	// with an empty name.
	childFolder := folder
	if o.XMLURL == "" && len(o.Outlines) > 0 {
		if label, ok := outlineLabel(o); ok {
			childFolder = &label
		}
		// else: keep the inherited `folder` (Rust's `.or(folder)`).
	}
	for i := range o.Outlines {
		collect(&o.Outlines[i], childFolder, out)
	}
}

// clonePtr returns a fresh *string copy so each Entry owns its folder value
// (mirrors Rust's folder.map(|s| s.to_string())).
func clonePtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

// outBuild mirrors a single <outline> on the export side, with `omitempty` on
// the optional attributes so a folder outline (no xmlUrl/type) does not emit
// empty xmlUrl="" / type="" attributes — keeping the output shape the same as
// the Rust `opml` crate's serializer (the input side uses its own `outline`
// struct without omitempty so it can detect presence).
type outBuild struct {
	XMLName  xml.Name   `xml:"outline"`
	Text     string     `xml:"text,attr"`
	Type     string     `xml:"type,attr,omitempty"`
	XMLURL   string     `xml:"xmlUrl,attr,omitempty"`
	Outlines []outBuild `xml:"outline"`
}

type docBuild struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    struct {
		Title string `xml:"title,omitempty"`
	} `xml:"head"`
	Body struct {
		Outlines []outBuild `xml:"outline"`
	} `xml:"body"`
}

// Build builds an OPML document from export feeds, grouping by folder name
// (feeds with a nil Folder sit at the top level). Feeds are bucketed by folder
// and the folders emitted in sorted order, with the folderless (top-level)
// feeds first — mirroring Rust's BTreeMap<Option<String>, _> iteration, where
// None sorts before any Some(name).
func Build(feeds []db.ExportFeed) (string, error) {
	type bucket struct {
		name     string
		outlines []outBuild
	}

	// Preserve first-seen order within each folder while grouping; the buckets
	// themselves are sorted by name below.
	index := make(map[string]int) // folder name → buckets index
	var folderless []outBuild
	var buckets []bucket

	for _, f := range feeds {
		// Skip entries with no feed URL: a feed with no xmlUrl has no meaning in
		// OPML and would not round-trip through Parse (collect requires xmlUrl),
		// so dropping it keeps Build symmetric with Parse.
		url := strings.TrimSpace(f.FeedURL)
		if url == "" {
			continue
		}
		o := outBuild{
			Text:   f.Title,
			XMLURL: url,
			Type:   "rss",
		}
		// Normalise the folder so empty/whitespace-only names land in the same
		// ungrouped (top-level) bucket as a nil Folder — mirroring Parse, where a
		// blank folder label resolves to ungrouped.
		if f.Folder == nil || strings.TrimSpace(*f.Folder) == "" {
			folderless = append(folderless, o)
			continue
		}
		name := strings.TrimSpace(*f.Folder)
		if idx, ok := index[name]; ok {
			buckets[idx].outlines = append(buckets[idx].outlines, o)
		} else {
			index[name] = len(buckets)
			buckets = append(buckets, bucket{name: name, outlines: []outBuild{o}})
		}
	}

	// Sort folders by name; None (folderless) comes first, as in Rust's BTreeMap.
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].name < buckets[j].name })

	var doc docBuild
	doc.Version = "2.0"
	doc.Head.Title = "Hive Subscriptions"
	doc.Body.Outlines = append(doc.Body.Outlines, folderless...)
	for _, b := range buckets {
		doc.Body.Outlines = append(doc.Body.Outlines, outBuild{
			Text:     b.name,
			Outlines: b.outlines,
		})
	}

	data, err := xml.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return "", apperr.Wrap("opml", err)
	}
	return xml.Header + string(data), nil
}
